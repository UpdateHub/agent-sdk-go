/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

package updatehub

import (
	"bufio"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type StateChangeListener struct {
	Listeners     map[string][]StateChangeCallback
	ErrorHandlers []ErrorCallback
}

type StateID string

const (
	StateIdle        = "idle"
	StatePoll        = "poll"
	StateProbe       = "probe"
	StateDownloading = "downloading"
	StateDownloaded  = "downloaded"
	StateInstalling  = "installing"
	StateInstalled   = "installed"
	StateExit        = "exit"
	StateError       = "error"
	StateRebooting   = "rebooting"
)

type Action string

const (
	ActionEnter = "enter"
	ActionLeave = "leave"
)

type StateChangeCallback func(action Action, state *State)
type ErrorCallback func(error string)

type State struct {
	ID   StateID
	conn net.Conn
}

// Cancel state
func (s State) Cancel() {
	s.conn.Write([]byte("cancel"))
}

// TryAgain state in `n` seconds
func (s State) TryAgain(n int) {
	s.conn.Write([]byte(strings.Join([]string{"try_again", strconv.Itoa(n)}, " ")))
}

// NewStateChangeListener instantiates a new StateChangeListener
func NewStateChangeListener() *StateChangeListener {
	return &StateChangeListener{
		Listeners: make(map[string][]StateChangeCallback),
	}
}

// On executes `cb` on enter or leave `state`
func (sc *StateChangeListener) On(action Action, state StateID, cb StateChangeCallback) {
	name := strings.Join([]string{string(action), "_", string(state)}, "")
	sc.Listeners[name] = append(sc.Listeners[name], cb)
}

// OnError executes `cb` on errors occurs
func (sc *StateChangeListener) OnError(cb ErrorCallback) {
	sc.ErrorHandlers = append(sc.ErrorHandlers, cb)
}

func (sc *StateChangeListener) emit(c net.Conn, action, state string) {
	name := strings.Join([]string{action, "_", state}, "")
	for _, cb := range sc.Listeners[name] {
		cb(Action(action), &State{ID: StateID(state), conn: c})
	}
}

func (sc *StateChangeListener) throwError(error string) {
	for _, cb := range sc.ErrorHandlers {
		cb(error)
	}
}

// Listen for state changes of updatehub agent
func (uh *StateChangeListener) Listen() error {
	_, err := os.Stat("/usr/share/updatehub/state-change-callbacks.d/10-updatehub-sdk-statechange-trigger")
	if err != nil && os.IsNotExist(err) {
		panic("updatehub-sdk-statechange-trigger not found!")
	}

	err = os.Remove("/run/updatehub-statechange.sock")
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	ln, err := net.Listen("unix", "/run/updatehub-statechange.sock")
	if err != nil {
		return err
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func(ln net.Listener, c chan os.Signal) {
		_ = <-c
		ln.Close()
		os.Exit(0)
	}(ln, sigc)

	for {
		fd, err := ln.Accept()
		if err != nil {
			log.Fatal("Accept error: ", err)
		}

		uh.handleConn(fd)
	}
}

func (uh *StateChangeListener) handleConn(c net.Conn) {
	buf := bufio.NewReader(c)

	for {
		bytes, err := buf.ReadBytes('\n')
		if err != nil {
			return
		}

		parts := strings.Split(strings.Trim(string(bytes), "\n"), " ")
		if parts[0] == "error" && len(parts) > 1 {
			uh.throwError(strings.Join(parts[1:], " "))
			c.Close()
			continue
		}

		if len(parts) < 2 {
			c.Close()
			continue
		}

		uh.emit(c, parts[0], parts[1])

		c.Close()
	}
}
