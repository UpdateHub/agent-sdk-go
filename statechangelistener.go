/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

package updatehub

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const SDKTriggerFilename string = "/usr/share/updatehub/state-change-callbacks.d/10-updatehub-sdk-statechange-trigger"
const SocketPath string = "updatehub-statechange.sock"

type StateChangeListener struct {
	Listeners     map[string][]StateChangeCallback
	ErrorHandlers []ErrorCallback
}

type StateID string

const (
	StateDownload = "download"
	StateInstall  = "install"
	StateReboot   = "reboot"
	StateError    = "error"
)

type StateChangeCallback func(state *State)
type ErrorCallback func(error string)

type State struct {
	ID   StateID
	conn net.Conn
}

// Downloading State
func (s State) Downloading() {
	s.conn.Write([]byte("downloading"))
}

// Rebooting State
func (s State) Rebooting() {
	s.conn.Write([]byte("rebooting"))
}

// Cancel state
func (s State) Cancel() {
	s.conn.Write([]byte("cancel"))
}

// Proceed to next state
func (s State) Proceed() {}

// NewStateChangeListener instantiates a new StateChangeListener
func NewStateChangeListener() *StateChangeListener {
	return &StateChangeListener{
		Listeners: make(map[string][]StateChangeCallback),
	}
}

// On executes `cb` on enter or leave `state`
func (sc *StateChangeListener) On(state StateID, cb StateChangeCallback) {
	name := strings.Join([]string{string(state)}, "")
	sc.Listeners[name] = append(sc.Listeners[name], cb)
}

// OnError executes `cb` on errors occurs
func (sc *StateChangeListener) OnError(cb ErrorCallback) {
	sc.ErrorHandlers = append(sc.ErrorHandlers, cb)
}

func (sc *StateChangeListener) emit(c net.Conn, state string) {
	name := strings.Join([]string{state}, "")
	for _, cb := range sc.Listeners[name] {
		cb(&State{ID: StateID(state), conn: c})
	}
}

func (sc *StateChangeListener) throwError(error string) {
	for _, cb := range sc.ErrorHandlers {
		cb(error)
	}
}

// Listen for state changes of updatehub agent
func (sc *StateChangeListener) Listen() error {
	file, err := os.Stat(SDKTriggerFilename)
	if err != nil && os.IsNotExist(err) {
		fmt.Println("WARNING: updatehub-sdk-statechange-trigger not found on", SDKTriggerFilename)
	}

	err = os.Remove(SDKTriggerFilename)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	ln, err := createListener(file)
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

		sc.handleConn(fd)
	}
}

func (sc *StateChangeListener) handleConn(c net.Conn) {
	buf := bufio.NewReader(c)

	for {
		bytes, err := buf.ReadBytes('\n')
		if err != nil {
			return
		}

		parts := strings.Split(strings.Trim(string(bytes), "\n"), " ")
		if parts[0] == "error" && len(parts) > 1 {
			sc.throwError(strings.Join(parts[1:], " "))
			c.Close()
			continue
		}

		if len(parts) < 1 {
			c.Close()
			continue
		}

		sc.emit(c, parts[0])

		c.Close()
	}
}

func createListener(file os.FileInfo) (net.Listener, error) {
	if file != nil {
		ln, err := net.Listen("unix", "/run/"+SocketPath)
		return ln, err
	}
	ln, err := net.Listen("unix", SocketPath)
	return ln, err
}
