/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

package updatehub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	return newTestClientWithTimeout(t, 5*time.Second, handler)
}

// newTestClientWithTimeout starts a test agent and returns a client for it. The
// server closes when the test ends, which is after the test's own deferred
// calls, so a handler that blocks is safe to release with a defer.
func newTestClientWithTimeout(t *testing.T, timeout time.Duration, handler http.HandlerFunc) *Client {
	t.Helper()

	// The hold outlasts any test, so a test that wants a turn back asks for it.
	return newTestClientWithHold(t, timeout, time.Minute, handler)
}

func newTestClientWithHold(t *testing.T, timeout, hold time.Duration, handler http.HandlerFunc) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL, timeout, hold)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

// trackPeak counts one request into the test agent and records the highest
// number it ever served at once. The returned call ends the request.
func trackPeak(inFlight, peak *atomic.Int32) func() {
	current := inFlight.Add(1)
	for {
		seen := peak.Load()
		if current <= seen || peak.CompareAndSwap(seen, current) {
			break
		}
	}

	return func() { inFlight.Add(-1) }
}

func TestNewClientRejectsUnusableConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		timeout time.Duration
		hold    time.Duration
	}{
		{"empty base URL", "", time.Second, time.Minute},
		{"no scheme", "localhost:8080", time.Second, time.Minute},
		{"no host", "http://", time.Second, time.Minute},
		{"zero timeout", DefaultBaseURL, 0, time.Minute},
		{"negative timeout", DefaultBaseURL, -time.Second, time.Minute},
		{"zero hold", DefaultBaseURL, time.Second, 0},
		{"negative hold", DefaultBaseURL, time.Second, -time.Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewClient(tc.baseURL, tc.timeout, tc.hold); err == nil {
				t.Fatalf("NewClient(%q, %v, %v) returned no error", tc.baseURL, tc.timeout, tc.hold)
			}
		})
	}
}

func TestProbeSendsNoBodyWithoutCustomServer(t *testing.T) {
	var (
		gotBody        []byte
		gotContentType string
	)

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = body
		gotContentType = r.Header.Get("Content-Type")

		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/probe" {
			t.Errorf("path = %q, want /probe", r.URL.Path)
		}

		_, _ = w.Write([]byte(`"no_update"`))
	})

	if _, err := client.Probe(context.Background(), ""); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if len(gotBody) != 0 {
		t.Errorf("request body = %q, want empty", gotBody)
	}
	if gotContentType != "" {
		t.Errorf("Content-Type = %q, want none", gotContentType)
	}
}

func TestProbeSendsCustomServerWhenGiven(t *testing.T) {
	var gotBody []byte

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`"updating"`))
	})

	if _, err := client.Probe(context.Background(), "http://example.com:8080"); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	var req struct {
		CustomServer string `json:"custom_server"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("unmarshal request body %q: %v", gotBody, err)
	}
	if req.CustomServer != "http://example.com:8080" {
		t.Errorf("custom_server = %q, want %q", req.CustomServer, "http://example.com:8080")
	}
}

func TestProbeDecodesEveryReplyShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ProbeResponse
	}{
		{"update available", `"updating"`, ProbeResponse{Outcome: ProbeUpdating}},
		{"no update", `"no_update"`, ProbeResponse{Outcome: ProbeNoUpdate}},
		{"back-off", `{"try_again":3600}`, ProbeResponse{Outcome: ProbeTryAgain, TryAgainIn: time.Hour}},
		{"busy downloading", `"download"`, ProbeResponse{Outcome: ProbeBusy, BusyState: StateDownload}},
		{"busy in the error state, which is not a failure", `"error"`, ProbeResponse{Outcome: ProbeBusy, BusyState: StateError}},
		{"busy installing", `"install"`, ProbeResponse{Outcome: ProbeBusy, BusyState: StateInstall}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})

			got, err := client.Probe(context.Background(), "")
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if got != tc.want {
				t.Errorf("Probe() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestProbeReturnsTheStatusCodeOnFailure(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Unhandled rejection: Client(UrlParse(RelativeUrlWithoutBase))"))
	})

	_, err := client.Probe(context.Background(), "")
	if err == nil {
		t.Fatal("Probe returned no error for a 500")
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", statusErr.StatusCode)
	}
	if statusErr.Body == "" {
		t.Error("StatusError carries no body")
	}
}

func TestProbeReturnsAnErrorForAnUndecodableBody(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})

	if _, err := client.Probe(context.Background(), ""); err == nil {
		t.Fatal("Probe returned no error for a body that is not JSON")
	}
}

func TestAReplyTooLargeToTrustIsAnErrorOfItsOwn(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append([]byte(`"`), bytes.Repeat([]byte("x"), (1<<20)+64)...))
	})

	_, err := client.Probe(context.Background(), "")
	if err == nil {
		t.Fatal("Probe returned no error for an oversized reply")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Errorf("error = %v, want it to name the size limit rather than a decode failure", err)
	}
}

func TestEveryMethodReturnsAnErrorWhenTheAgentIsDown(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	client, err := NewClient(url, 2*time.Second, time.Minute)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	calls := map[string]func() error{
		"Probe":         func() error { _, err := client.Probe(ctx, ""); return err },
		"GetInfo":       func() error { _, err := client.GetInfo(ctx); return err },
		"GetLogs":       func() error { _, err := client.GetLogs(ctx); return err },
		"LocalInstall":  func() error { _, err := client.LocalInstall(ctx, "/tmp/x.uhupkg"); return err },
		"RemoteInstall": func() error { _, err := client.RemoteInstall(ctx, "https://example.com/x.uhupkg"); return err },
		"AbortDownload": func() error { _, err := client.AbortDownload(ctx); return err },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked: %v", name, r)
				}
			}()

			if err := call(); err == nil {
				t.Fatalf("%s returned no error with no agent listening", name)
			}
		})
	}
}

func TestClientSerialisesEveryCall(t *testing.T) {
	var (
		inFlight atomic.Int32
		peak     atomic.Int32
	)

	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		defer trackPeak(&inFlight, &peak)()

		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte(`"no_update"`))
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := client.Probe(context.Background(), ""); err != nil {
				t.Errorf("Probe: %v", err)
			}
		})
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Errorf("peak concurrent requests = %d, want 1", got)
	}
}

func TestAnAbandonedCallKeepsTheNextOneOutOfTheAgent(t *testing.T) {
	var (
		inFlight atomic.Int32
		peak     atomic.Int32
		first    = make(chan struct{})
		served   atomic.Int32
	)

	// The first request outlives the caller that sent it; every later one runs
	// straight through.
	release := sync.OnceFunc(func() { close(first) })

	defer release()

	client := newTestClientWithTimeout(t, 100*time.Millisecond, func(w http.ResponseWriter, _ *http.Request) {
		defer trackPeak(&inFlight, &peak)()

		if served.Add(1) == 1 {
			<-first
		}

		_, _ = w.Write([]byte(`"no_update"`))
	})

	if _, err := client.Probe(context.Background(), ""); err == nil {
		t.Fatal("the first Probe returned no error, want its deadline to bound it")
	}

	// The agent is still holding the abandoned request. The second call must
	// wait for it rather than arrive beside it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	answered := make(chan error, 1)
	go func() {
		_, err := client.Probe(ctx, "")
		answered <- err
	}()

	select {
	case <-answered:
		t.Fatal("the second Probe ran while the first was still with the agent")
	case <-time.After(200 * time.Millisecond):
	}

	release()

	select {
	case err := <-answered:
		if err != nil {
			t.Fatalf("the second Probe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Probe never ran")
	}

	if got := peak.Load(); got != 1 {
		t.Errorf("peak concurrent requests = %d, want 1", got)
	}
}

func TestACallThatNeverReachesTheAgentSaysSo(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	client := newTestClientWithTimeout(t, 100*time.Millisecond, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`"no_update"`))
	})

	first, err := client.Probe(context.Background(), "")
	if err == nil {
		t.Fatalf("the first Probe returned %+v, want its deadline to bound it", first)
	}
	if errors.Is(err, ErrCallOutstanding) {
		t.Error("the first Probe reported ErrCallOutstanding; it did reach the agent")
	}

	_, err = client.Probe(context.Background(), "")
	if !errors.Is(err, ErrCallOutstanding) {
		t.Errorf("the second Probe returned %v, want ErrCallOutstanding", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the second Probe returned %v, want it to carry the deadline too", err)
	}
}

func TestTheHoldGivesTheTurnBackWhenTheAgentNeverAnswers(t *testing.T) {
	var (
		first  = make(chan struct{})
		served atomic.Int32
	)

	release := sync.OnceFunc(func() { close(first) })
	defer release()

	client := newTestClientWithHold(t, 50*time.Millisecond, 100*time.Millisecond,
		func(w http.ResponseWriter, _ *http.Request) {
			// Only the first request goes unanswered. A later one must not
			// queue behind it inside the handler.
			if served.Add(1) == 1 {
				<-first
			}

			_, _ = w.Write([]byte(`"no_update"`))
		})

	if _, err := client.Probe(context.Background(), ""); err == nil {
		t.Fatal("the first Probe returned no error, want its deadline to bound it")
	}

	// The agent never answered the abandoned request, so only the hold can
	// bring the turn back.
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.Probe(ctx, ""); err != nil {
		t.Fatalf("the second Probe: %v", err)
	}
}

func TestTwoClientsForOneAgentShareTheTurn(t *testing.T) {
	var (
		inFlight atomic.Int32
		peak     atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer trackPeak(&inFlight, &peak)()

		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`"no_update"`))
	}))
	defer server.Close()

	var clients []*Client
	for range 2 {
		client, err := NewClient(server.URL, 5*time.Second, time.Minute)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		clients = append(clients, client)
	}

	if clients[0] == clients[1] {
		t.Fatal("NewClient returned the same client twice; the test proves nothing")
	}

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Go(func() {
			if _, err := client.Probe(context.Background(), ""); err != nil {
				t.Errorf("Probe: %v", err)
			}
		})
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Errorf("peak concurrent requests = %d, want 1; two clients for one agent must share the turn", got)
	}
}

func TestTheTimeoutBoundsACall(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	client := newTestClientWithTimeout(t, 50*time.Millisecond, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`"no_update"`))
	})

	start := time.Now()
	if _, err := client.Probe(context.Background(), ""); err == nil {
		t.Fatal("Probe returned no error for a hung agent")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Probe blocked for %v, want the 50ms timeout to bound it", elapsed)
	}
}

func TestACallerDeadlineOverridesTheClientTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	client := newTestClientWithTimeout(t, time.Hour, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`"no_update"`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := client.Probe(ctx, ""); err == nil {
		t.Fatal("Probe returned no error for a hung agent")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Probe blocked for %v, want the caller deadline to bound it", elapsed)
	}
}

func TestLocalInstallReadsTheRequestAndTheReply(t *testing.T) {
	var gotBody []byte

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/local_install" {
			t.Errorf("path = %q, want /local_install", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`"probe"`))
	})

	got, err := client.LocalInstall(context.Background(), "/tmp/update.uhupkg")
	if err != nil {
		t.Fatalf("LocalInstall: %v", err)
	}
	if !got.Accepted {
		t.Error("Accepted = false, want true for a 200")
	}
	if got.State != StateProbe {
		t.Errorf("State = %q, want %q", got.State, StateProbe)
	}

	var req struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("unmarshal request body %q: %v", gotBody, err)
	}
	if req.File != "/tmp/update.uhupkg" {
		t.Errorf("file = %q, want %q", req.File, "/tmp/update.uhupkg")
	}
}

func TestLocalInstallReportsARefusalWithoutAnError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotAcceptable)
		_, _ = w.Write([]byte(`"install"`))
	})

	got, err := client.LocalInstall(context.Background(), "/tmp/update.uhupkg")
	if err != nil {
		t.Fatalf("LocalInstall returned an error for a 406: %v", err)
	}
	if got.Accepted {
		t.Error("Accepted = true, want false for a 406")
	}
	if got.State != StateInstall {
		t.Errorf("State = %q, want %q", got.State, StateInstall)
	}
}

func TestLocalInstallReturnsTheStatusCodeOnAnUnexpectedFailure(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Request body deserialize error"))
	})

	_, err := client.LocalInstall(context.Background(), "/tmp/update.uhupkg")
	if err == nil {
		t.Fatal("LocalInstall returned no error for a 400")
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", statusErr.StatusCode)
	}
}

func TestRemoteInstallSendsTheURL(t *testing.T) {
	var gotBody []byte

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/remote_install" {
			t.Errorf("path = %q, want /remote_install", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`"entry_point"`))
	})

	got, err := client.RemoteInstall(context.Background(), "https://example.com/update.uhupkg")
	if err != nil {
		t.Fatalf("RemoteInstall: %v", err)
	}
	if !got.Accepted || got.State != StateEntryPoint {
		t.Errorf("RemoteInstall() = %+v, want an accepted entry_point", got)
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("unmarshal request body %q: %v", gotBody, err)
	}
	if req.URL != "https://example.com/update.uhupkg" {
		t.Errorf("url = %q, want %q", req.URL, "https://example.com/update.uhupkg")
	}
}

func TestAbortDownloadReportsBothReplies(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/update/download/abort" {
				t.Errorf("path = %q, want /update/download/abort", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"message":"request accepted, download aborted"}`))
		})

		got, err := client.AbortDownload(context.Background())
		if err != nil {
			t.Fatalf("AbortDownload: %v", err)
		}
		if !got.Accepted {
			t.Error("Accepted = false, want true")
		}
		if got.Message != "request accepted, download aborted" {
			t.Errorf("Message = %q", got.Message)
		}
	})

	t.Run("refused", func(t *testing.T) {
		client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = w.Write([]byte(`{"error":"there is no download to be aborted"}`))
		})

		got, err := client.AbortDownload(context.Background())
		if err != nil {
			t.Fatalf("AbortDownload returned an error for a 406: %v", err)
		}
		if got.Accepted {
			t.Error("Accepted = true, want false")
		}
		if got.Message != "there is no download to be aborted" {
			t.Errorf("Message = %q", got.Message)
		}
	})
}

func TestGetInfoReadsTheAgentVersion(t *testing.T) {
	const body = `{
		"state": "park",
		"version": "2.1.6",
		"config": {
			"firmware": {"metadata": "/usr/share/updatehub"},
			"network": {"server_address": "https://api.updatehub.io", "listen_socket": "localhost:8080"},
			"polling": {"interval": "1h", "enabled": true},
			"storage": {"read_only": false, "runtime_settings": "/var/lib/updatehub/runtime_settings.conf"},
			"update": {"download_dir": "/tmp", "supported_install_modes": ["copy", "raw"]}
		},
		"firmware": {
			"product_uid": "0123",
			"version": "8.0.1",
			"hardware": "ema40i",
			"pub_key": null,
			"device_identity": {"id1": "value", "id2": ["a", "b"]},
			"device_attributes": {}
		},
		"runtime_settings": {
			"polling": {
				"last": "2026-08-27T12:00:00Z",
				"retries": 0,
				"now": false,
				"server_address": "default"
			},
			"update": {},
			"path": "/var/lib/updatehub/runtime_settings.conf",
			"persistent": true
		}
	}`

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/info" {
			t.Errorf("path = %q, want /info", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})

	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.Version != "2.1.6" {
		t.Errorf("Version = %q, want 2.1.6", info.Version)
	}
	if info.State != StatePark {
		t.Errorf("State = %q, want park", info.State)
	}
	if got := info.Config.Polling.Interval; got != "1h" {
		t.Errorf("Config.Polling.Interval = %q, want 1h", got)
	}
	if got := info.Firmware.DeviceIdentity["id1"]; len(got) != 1 || got[0] != "value" {
		t.Errorf("DeviceIdentity[id1] = %q, want [value]", got)
	}
	if got := info.Firmware.DeviceIdentity["id2"]; len(got) != 2 {
		t.Errorf("DeviceIdentity[id2] = %q, want two values", got)
	}
	if !info.RuntimeSettings.Polling.ServerAddress.IsDefault() {
		t.Error("ServerAddress.IsDefault() = false, want true")
	}
	if got := info.RuntimeSettings.Polling.ServerAddress; got != "" {
		t.Errorf("ServerAddress = %q, want empty for the default", got)
	}
}

func TestGetInfoReadsACustomServerAddress(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.1.6","runtime_settings":{"polling":{"server_address":{"custom":"https://example.com"}}}}`))
	})

	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}

	address := info.RuntimeSettings.Polling.ServerAddress
	if address.IsDefault() {
		t.Fatal("IsDefault() = true, want false")
	}
	if address != "https://example.com" {
		t.Errorf("ServerAddress = %q, want https://example.com", address)
	}
}

func TestGetLogsDecodesTheEntries(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/log" {
			t.Errorf("path = %q, want /log", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"entries":[{"level":"info","message":"probing","time":"2026-08-27T12:00:00Z","data":{"k":"v"}}],"first_index":7}`))
	})

	logs, err := client.GetLogs(context.Background())
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(logs.Entries))
	}
	if logs.Entries[0].Message != "probing" {
		t.Errorf("Message = %q, want probing", logs.Entries[0].Message)
	}
	if logs.Entries[0].Data["k"] != "v" {
		t.Errorf("Data = %v, want k=v", logs.Entries[0].Data)
	}
	if logs.FirstIndex != 7 {
		t.Errorf("FirstIndex = %d, want 7", logs.FirstIndex)
	}
}
