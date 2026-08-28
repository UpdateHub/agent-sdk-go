/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

package updatehub

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// socketPath returns a short path under the test's own directory. A Unix socket
// address is bounded to about 100 bytes, and the default temporary directory of
// a test can be longer than that.
func socketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "uh")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, "s.sock")
}

// serve binds the listener and serves it until the test ends. The returned call
// stops it earlier, and waits for Serve to return.
func serve(t *testing.T, sc *StateChange) (stop func()) {
	t.Helper()

	if err := sc.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- sc.Serve(ctx) }()

	stop = sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the context was cancelled")
		}
		_ = sc.Close()
	})
	t.Cleanup(stop)

	return stop
}

// ask plays the agent's trigger script: it writes one state name and reads
// whatever the listener answers before the connection closes.
func ask(t *testing.T, path, state string) string {
	t.Helper()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if _, err := conn.Write([]byte(state + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	return string(reply)
}

func TestTheListenerCancelsTheStateItWasAskedTo(t *testing.T) {
	path := socketPath(t)
	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error { return h.Cancel() })
	sc.OnState(StateInstall, func(_ context.Context, h *Handler) error { return h.Proceed() })
	serve(t, sc)

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q, want cancel", got)
	}
	if got := ask(t, path, "install"); got != "" {
		t.Errorf("install answered %q, want nothing", got)
	}
}

func TestTheHandlerReportsTheStateItWasCalledFor(t *testing.T) {
	path := socketPath(t)
	seen := make(chan State, 1)

	sc := NewStateChange(path)
	sc.OnState(StateReboot, func(_ context.Context, h *Handler) error {
		seen <- h.State()
		return h.Proceed()
	})
	serve(t, sc)

	ask(t, path, "reboot")

	select {
	case got := <-seen:
		if got != StateReboot {
			t.Errorf("State() = %q, want reboot", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the callback did not run")
	}
}

func TestAStateWithNoCallbackIsAnsweredByClosing(t *testing.T) {
	path := socketPath(t)
	sc := NewStateChange(path)
	serve(t, sc)

	if got := ask(t, path, "probe"); got != "" {
		t.Errorf("probe answered %q, want nothing", got)
	}
}

func TestABareNewlineIsAnsweredByClosing(t *testing.T) {
	path := socketPath(t)
	called := make(chan struct{}, 1)

	sc := NewStateChange(path)
	sc.OnState("", func(_ context.Context, h *Handler) error {
		called <- struct{}{}
		return h.Proceed()
	})
	serve(t, sc)

	if got := ask(t, path, ""); got != "" {
		t.Errorf("a bare newline answered %q, want nothing", got)
	}

	select {
	case <-called:
		t.Error("a bare newline reached a callback, want no dispatch")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTheListenerServesConnectionsConcurrently(t *testing.T) {
	path := socketPath(t)
	release := make(chan struct{})
	// The callback blocks, and Serve waits for it, so the release must happen
	// before the cleanup that stops Serve.
	defer close(release)

	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error {
		<-release
		return h.Cancel()
	})
	sc.OnState(StateInstall, func(_ context.Context, h *Handler) error { return h.Proceed() })
	serve(t, sc)

	blocked, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = blocked.Close() }()
	if _, err := blocked.Write([]byte("download\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	answered := make(chan string, 1)
	go func() { answered <- ask(t, path, "install") }()

	select {
	case got := <-answered:
		if got != "" {
			t.Errorf("install answered %q, want nothing", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a blocked callback stopped the listener from serving a second connection")
	}
}

func TestBindReturnsEADDRINUSEAndKeepsTheLiveListenerServing(t *testing.T) {
	path := socketPath(t)

	first := NewStateChange(path)
	first.OnState(StateDownload, func(_ context.Context, h *Handler) error { return h.Cancel() })
	serve(t, first)

	second := NewStateChange(path)
	err := second.Bind()
	if err == nil {
		_ = second.Close()
		t.Fatal("the second Bind succeeded, want EADDRINUSE")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Bind error = %v, want EADDRINUSE", err)
	}

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q after a failed second bind, want cancel", got)
	}
}

func TestBindDoesNotUnlinkAStaleSocket(t *testing.T) {
	path := socketPath(t)

	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if unix, ok := stale.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sc := NewStateChange(path)
	if err := sc.Bind(); err == nil {
		_ = sc.Close()
		t.Fatal("Bind removed a socket that was already there, want EADDRINUSE")
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("the socket file is gone: %v", err)
	}
}

func TestServeWithoutBindReturnsAnError(t *testing.T) {
	sc := NewStateChange(socketPath(t))

	if err := sc.Serve(context.Background()); err == nil {
		t.Fatal("Serve returned no error with nothing bound")
	}
}

func TestBindTwiceReturnsAnError(t *testing.T) {
	sc := NewStateChange(socketPath(t))
	if err := sc.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	defer func() { _ = sc.Close() }()

	if err := sc.Bind(); err == nil {
		t.Fatal("the second Bind on the same listener returned no error")
	}
}

func TestCloseStopsServe(t *testing.T) {
	sc := NewStateChange(socketPath(t))
	if err := sc.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sc.Serve(context.Background()) }()

	// Serve may not have reached Accept yet; Close is safe either way.
	if err := sc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v after Close, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Close")
	}

	if err := sc.Close(); err != nil {
		t.Errorf("the second Close returned %v, want nil", err)
	}
}

func TestServeAfterCloseStopsWithoutFaulting(t *testing.T) {
	sc := NewStateChange(socketPath(t))
	if err := sc.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A Close that wins the race against a starting Serve is an ordinary stop,
	// not a fault. Only a listener that was never bound is an error.
	if err := sc.Serve(context.Background()); err != nil {
		t.Fatalf("Serve returned %v on a closed listener, want nil", err)
	}
}

func TestACloseIsFollowedByABind(t *testing.T) {
	path := socketPath(t)

	sc := NewStateChange(path)
	if err := sc.Bind(); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error { return h.Cancel() })
	serve(t, sc)

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q after a rebind, want cancel", got)
	}
}

func TestACallbackErrorReachesTheSinkAndTheListenerKeepsServing(t *testing.T) {
	path := socketPath(t)
	failure := errors.New("the participant is not ready")
	reported := make(chan error, 4)

	sc := NewStateChange(path)
	sc.OnError(func(err error) { reported <- err })
	sc.OnState(StateDownload, func(_ context.Context, _ *Handler) error { return failure })
	sc.OnState(StateInstall, func(_ context.Context, h *Handler) error { return h.Proceed() })
	serve(t, sc)

	ask(t, path, "download")

	select {
	case err := <-reported:
		if !errors.Is(err, failure) {
			t.Errorf("the sink got %v, want %v", err, failure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the callback error never reached the sink")
	}

	if got := ask(t, path, "install"); got != "" {
		t.Errorf("install answered %q after a callback error, want nothing", got)
	}
}

func TestACallbackErrorWithNoSinkDoesNotKillTheProcess(t *testing.T) {
	path := socketPath(t)

	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(_ context.Context, _ *Handler) error {
		return errors.New("the participant is not ready")
	})
	serve(t, sc)

	if got := ask(t, path, "download"); got != "" {
		t.Errorf("download answered %q, want nothing", got)
	}
}

func TestACallbackPanicReachesTheSinkAndTheListenerKeepsServing(t *testing.T) {
	path := socketPath(t)
	reported := make(chan error, 4)

	sc := NewStateChange(path)
	sc.OnError(func(err error) { reported <- err })
	sc.OnState(StateDownload, func(_ context.Context, _ *Handler) error {
		panic("the participant lost its bridge")
	})
	sc.OnState(StateInstall, func(_ context.Context, h *Handler) error { return h.Proceed() })
	serve(t, sc)

	ask(t, path, "download")

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "the participant lost its bridge") {
			t.Errorf("the sink got %v, want the panic value", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the panic never reached the sink")
	}

	if got := ask(t, path, "install"); got != "" {
		t.Errorf("install answered %q after a callback panic, want nothing", got)
	}
}

func TestAPanickingCallbackStillAnswersWhatItDeferred(t *testing.T) {
	path := socketPath(t)

	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error {
		defer h.Cancel() //nolint:errcheck // the deferred answer is the point

		panic("the participant lost its bridge")
	})
	serve(t, sc)

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q, want the answer the callback deferred", got)
	}
}

func TestAFailedVetoReachesTheSinkEvenWhenTheCallbackDropsIt(t *testing.T) {
	path := socketPath(t)
	reported := make(chan error, 1)
	hungUp := make(chan struct{})

	sc := NewStateChange(path)
	sc.OnError(func(err error) { reported <- err })
	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error {
		<-hungUp

		// The peer is gone, so the veto cannot be written. The callback drops
		// the error on purpose: the listener must report it anyway.
		_ = h.Cancel()

		return nil
	})
	serve(t, sc)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := conn.Write([]byte("download\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// The state name is already in the socket buffer, so the listener still
	// reads it and still runs the callback.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(hungUp)

	select {
	case err := <-reported:
		if err == nil {
			t.Error("the sink got a nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the failed veto never reached the sink")
	}

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q after a lost peer, want cancel", got)
	}
}

func TestReplyingTwiceReturnsAnError(t *testing.T) {
	path := socketPath(t)
	second := make(chan error, 1)

	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(_ context.Context, h *Handler) error {
		if err := h.Cancel(); err != nil {
			return err
		}
		second <- h.Proceed()

		return nil
	})
	serve(t, sc)

	if got := ask(t, path, "download"); got != "cancel" {
		t.Errorf("download answered %q, want cancel", got)
	}

	select {
	case err := <-second:
		if !errors.Is(err, ErrAlreadyReplied) {
			t.Errorf("the second reply returned %v, want ErrAlreadyReplied", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the callback did not run")
	}
}

func TestTheCallbackSeesTheServeContext(t *testing.T) {
	path := socketPath(t)
	seen := make(chan context.Context, 1)

	sc := NewStateChange(path)
	sc.OnState(StateDownload, func(ctx context.Context, h *Handler) error {
		seen <- ctx
		return h.Proceed()
	})
	stop := serve(t, sc)

	ask(t, path, "download")

	var callbackCtx context.Context
	select {
	case callbackCtx = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the callback did not run")
	}

	if callbackCtx.Err() != nil {
		t.Errorf("the callback context was already done: %v", callbackCtx.Err())
	}

	stop()

	if callbackCtx.Err() == nil {
		t.Error("the callback context outlived Serve")
	}
}

func TestTriggerInstalledReportsPresence(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "absent")
	installed, err := TriggerInstalled(missing)
	if err != nil {
		t.Fatalf("TriggerInstalled: %v", err)
	}
	if installed {
		t.Error("a missing trigger reported as installed")
	}

	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	installed, err = TriggerInstalled(present)
	if err != nil {
		t.Fatalf("TriggerInstalled: %v", err)
	}
	if !installed {
		t.Error("an installed trigger reported as missing")
	}
}

func TestTheTriggerAndSocketPathsAreTheAgentsOwn(t *testing.T) {
	const want = "/usr/share/updatehub/state-change-callbacks.d/10-updatehub-sdk-statechange-trigger"

	if TriggerPath != want {
		t.Errorf("TriggerPath = %q, want %q", TriggerPath, want)
	}
	if DefaultSocketPath != "/run/updatehub-statechange.sock" {
		t.Errorf("DefaultSocketPath = %q", DefaultSocketPath)
	}
}
