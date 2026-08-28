/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

package updatehub

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultSocketPath is the state-change socket the agent's trigger script
	// connects to.
	DefaultSocketPath = "/run/updatehub-statechange.sock"

	// TriggerPath is the script the agent runs before every state transition.
	// The script connects to the state-change socket and hands the agent
	// whatever the socket answers. Without it the agent never consults the
	// socket at all, so a listener that is bound and correct still vetoes
	// nothing. Read TriggerInstalled to find out.
	TriggerPath = "/usr/share/updatehub/state-change-callbacks.d/10-updatehub-sdk-statechange-trigger"
)

// readBufferSize holds the longest state name the agent sends, with room to
// spare. A longer line still reads correctly; it takes one more fill.
const readBufferSize = 64

// cancelReply is the exact byte string the agent reads as a veto. Anything else
// it reads, including a trailing newline, is an error it also treats as a
// cancel, so the reply carries no newline.
const cancelReply = "cancel"

// ErrAlreadyReplied reports a second answer to one state change. The agent
// reads one reply per connection.
var ErrAlreadyReplied = errors.New("updatehub: the state change was already answered")

// CallbackFunc answers one state change.
//
// The callback must answer through the handler, by calling Cancel to veto the
// transition or Proceed to allow it. The listener closes the connection once
// the callback returns, and a connection that closes with nothing written lets
// the agent proceed — so a callback that returns an error without answering
// still lets the transition through.
//
// ctx is the context Serve runs under. It is cancelled when Serve returns.
//
// The listener runs one callback per connection, in its own goroutine, so a
// slow callback delays only the agent transition it answers. Callbacks for
// different connections can run at the same time. A callback that panics is
// reported to the sink OnError registers, like one that returns an error.
type CallbackFunc func(ctx context.Context, handler *Handler) error

// Handler answers one state change. It is not safe for concurrent use.
type Handler struct {
	conn     net.Conn
	state    State
	replied  bool
	writeErr error
}

// State reports the state the agent is about to enter.
func (h *Handler) State() State { return h.state }

// Cancel vetoes the transition. The agent then resets its machine and enters
// its entry point again.
func (h *Handler) Cancel() error {
	if h.replied {
		return ErrAlreadyReplied
	}
	h.replied = true

	if _, err := h.conn.Write([]byte(cancelReply)); err != nil {
		h.writeErr = fmt.Errorf("updatehub: answer the %s state change: %w", h.state, err)

		return h.writeErr
	}

	return nil
}

// Proceed lets the transition happen. It writes nothing, which is how the agent
// reads consent.
func (h *Handler) Proceed() error {
	if h.replied {
		return ErrAlreadyReplied
	}
	h.replied = true

	return nil
}

// StateChange serves the agent's state-change socket.
//
// The agent connects to the socket before every transition and blocks until it
// gets an answer, so an unbound socket is not a degraded veto: the trigger
// script prints nothing, and the agent reads that as consent.
//
// Register the callbacks first, then Bind, then Serve. Bind and Serve are
// separate because a bind that fails with EADDRINUSE is a decision the caller
// must make — another process holds the socket, or a previous process left the
// path behind — and this package never makes it for them.
//
// A StateChange is safe for concurrent use.
type StateChange struct {
	socketPath string

	mu        sync.Mutex
	callbacks map[State]CallbackFunc
	onError   func(error)
	listener  net.Listener
	closed    bool

	handlers sync.WaitGroup
}

// NewStateChange returns a listener for the socket at socketPath. Pass
// DefaultSocketPath for the ordinary case.
func NewStateChange(socketPath string) *StateChange {
	return &StateChange{
		socketPath: socketPath,
		callbacks:  make(map[State]CallbackFunc),
	}
}

// OnState registers the callback for one state. A second registration for the
// same state replaces the first. A state with no callback is answered by
// closing the connection, which lets the agent proceed.
func (sc *StateChange) OnState(state State, f CallbackFunc) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.callbacks[state] = f
}

// OnError registers the sink for the errors one connection raises: a failed
// read, a callback error, or a reply that could not be written. Serve keeps
// serving after each of them, because a listener that stops leaves the agent
// blocked on the next transition.
//
// The sink is called from the goroutine that serves the connection, so it can
// run for several connections at once. A listener with no sink discards these
// errors: this package writes nothing to stdout and nothing through log.
func (sc *StateChange) OnError(f func(error)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.onError = f
}

// Bind binds the socket.
//
// It never unlinks the path first. A path that is already there fails with
// EADDRINUSE, which errors.Is reports through the returned error, and the
// caller decides: a live binder means another process holds the agent's only
// veto channel, while a stale path from a dead process is the caller's to
// remove before it binds again.
func (sc *StateChange) Bind() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.listener != nil {
		return fmt.Errorf("updatehub: %q is already bound", sc.socketPath)
	}

	listener, err := net.Listen("unix", sc.socketPath)
	if err != nil {
		return fmt.Errorf("updatehub: bind %q: %w", sc.socketPath, err)
	}
	sc.listener = listener
	sc.closed = false

	return nil
}

// Serve accepts connections and answers them until Close is called or ctx is
// done, and then waits for the callbacks still running.
//
// It returns nil for a stop the caller asked for, which includes a Close that
// arrives before it starts, and an error for a stop it did not ask for: a
// failed Accept, or a Serve on a listener that was never bound. A single
// connection never stops it; those errors reach the sink OnError registers.
func (sc *StateChange) Serve(ctx context.Context) error {
	sc.mu.Lock()
	listener, closed := sc.listener, sc.closed
	sc.mu.Unlock()

	if listener == nil {
		if closed {
			return nil
		}

		return fmt.Errorf("updatehub: serve %q: bind it first", sc.socketPath)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer context.AfterFunc(ctx, func() { _ = sc.Close() })()

	var failure error

	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				failure = fmt.Errorf("updatehub: accept on %q: %w", sc.socketPath, err)
			}

			break
		}

		sc.handlers.Go(func() {
			sc.handle(ctx, conn)
		})
	}

	sc.handlers.Wait()

	return failure
}

// Close stops Serve and releases the socket. It is safe to call more than once,
// and safe to call on a listener that was never bound. A closed listener can be
// bound again.
func (sc *StateChange) Close() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	listener := sc.listener
	if listener == nil {
		return nil
	}
	sc.listener = nil
	sc.closed = true

	if err := listener.Close(); err != nil {
		return fmt.Errorf("updatehub: close %q: %w", sc.socketPath, err)
	}

	return nil
}

// handle reads one state name and answers it. The agent's trigger script writes
// the name with a newline after it, then reads until the connection closes.
func (sc *StateChange) handle(ctx context.Context, conn net.Conn) {
	var state State

	defer func() { _ = conn.Close() }()

	// A peer that connects and writes nothing would park this goroutine, and
	// Serve waits for it, so the read gives up when Serve does.
	defer context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })()

	// A callback runs in a goroutine this package owns, so a panic in one would
	// take the whole process down and the consumer could not recover it. It
	// becomes an error on the sink instead. Any deferred answer the callback
	// registered has already run by now.
	defer func() {
		if raised := recover(); raised != nil {
			sc.report(fmt.Errorf("updatehub: the %q callback panicked: %v", state, raised))
		}
	}()

	line, err := bufio.NewReaderSize(conn, readBufferSize).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		sc.report(fmt.Errorf("updatehub: read a state name from %q: %w", sc.socketPath, err))

		return
	}

	state = State(strings.TrimSpace(line))
	if state == "" {
		return
	}

	callback := sc.callbackFor(state)
	if callback == nil {
		return
	}

	handler := &Handler{conn: conn, state: state}

	switch err := callback(ctx, handler); {
	case err != nil:
		sc.report(err)
	case handler.writeErr != nil:
		sc.report(handler.writeErr)
	}
}

func (sc *StateChange) callbackFor(state State) CallbackFunc {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	return sc.callbacks[state]
}

func (sc *StateChange) report(err error) {
	sc.mu.Lock()
	sink := sc.onError
	sc.mu.Unlock()

	if sink != nil {
		sink(err)
	}
}

// TriggerInstalled reports whether the agent's trigger script is at path. Pass
// TriggerPath for the ordinary case.
//
// Without the script the agent never consults the socket, so it installs
// whatever the server offers, on every poll, unasked. What to do about that is
// the caller's decision.
func TriggerInstalled(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("updatehub: stat %q: %w", path, err)
	}

	return true, nil
}
