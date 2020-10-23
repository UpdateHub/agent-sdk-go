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
	"strings"
)

const SDKTriggerFilename string = "/usr/share/updatehub/state-change-callbacks.d/10-updatehub-sdk-statechange-trigger"
const SocketPath string = "/run/updatehub-statechange.sock"

// CallbackFunc the type of the callbacks.
type CallbackFunc func(handler *Handler)

// StateChange struct that store the callbacks for a state.
type StateChange struct {
	Listeners map[string][]CallbackFunc
}

// State Represent the states of UpdateHub Agent can handle.
type State string

const (
	StateProbe    = "probe"
	StateDownload = "download"
	StateInstall  = "install"
	StateReboot   = "reboot"
	StateError    = "error"
)

/// Handler used to communicate with UpdateHub
/// to call commands on the state callbacks.
type Handler struct {
	conn net.Conn
}

// Cancel cancels the current state.
func (h Handler) Cancel() {
	_, err := h.conn.Write([]byte("cancel"))
	checkErr(err)
}

// Proceed proceeds to the next state.
func (h Handler) Proceed() {}

// NewStateChange instantiates a new StateChange.
func NewStateChange() *StateChange {
	return &StateChange{
		Listeners: make(map[string][]CallbackFunc),
	}
}

// OnState register the callbacks for a state passed as argument.
func (sc *StateChange) OnState(state State, f CallbackFunc) {
	name := strings.Join([]string{string(state)}, "")
	sc.Listeners[name] = append(sc.Listeners[name], f)
}

/// Listen start the agent to listen for messages on the socket.
func (sc *StateChange) Listen() error {
	_, err := os.Stat(SDKTriggerFilename)
	if err != nil && os.IsNotExist(err) {
		fmt.Println("WARNING: updatehub-sdk-statechange-trigger not found on", SDKTriggerFilename)
	}

	ln, err := createListener()
	checkErr(err)

	for {
		conn, err := ln.Accept()
		checkErr(err)

		sc.handleConn(conn)
	}
}

func (sc *StateChange) handleConn(c net.Conn) {
	buf := bufio.NewReader(c)

	for {
		bytes, err := buf.ReadBytes('\n')
		if err != nil {
			return
		}

		sc.emit(c, strings.Trim(string(bytes), "\n"))
		c.Close()
	}
}

func (sc *StateChange) emit(c net.Conn, state string) {
	for _, f := range sc.Listeners[strings.Join([]string{state}, "")] {
		f(&Handler{conn: c})
	}
}

func createListener() (net.Listener, error) {
	socketEnv := os.Getenv("UH_LISTENER_TEST")
	if len(socketEnv) == 0 {
		removeFile(SocketPath)

		ln, err := net.Listen("unix", SocketPath)
		return ln, err
	}
	removeFile(socketEnv)

	ln, err := net.Listen("unix", socketEnv)
	return ln, err
}

func removeFile(file string) {
	_, err := os.Stat(file)
	if err == nil && !os.IsNotExist(err) {
		err := os.Remove(file)
		checkErr(err)
	}
}

func checkErr(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
