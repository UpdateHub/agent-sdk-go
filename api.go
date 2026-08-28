/*
UpdateHub
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/

// Package updatehub is a client for the UpdateHub agent's local API.
//
// It speaks two transports. Client calls the agent's HTTP API, by default on
// localhost:8080. StateChange serves the state-change socket the agent connects
// to before every transition, so a consumer can veto one.
//
// The package is validated against UpdateHub agent 2.1.6.
//
// Two properties hold across the whole package, because the intended consumer
// is a single-process firmware daemon on a device with no way back. No call
// panics, and no call writes to stdout or through the log package: every
// failure is returned to the caller, which decides what it means.
package updatehub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is where the agent serves its HTTP API out of the box.
const DefaultBaseURL = "http://localhost:8080"

// maxResponseBytes bounds a reply, so a broken peer cannot exhaust the
// consumer's memory. A larger reply is an error of its own rather than a body
// cut short, which would reach the caller as a decode failure and read as an
// agent that answered nonsense.
const maxResponseBytes = 1 << 20

// State is a state of the UpdateHub agent's machine. The agent reports one in
// three places: the /info reply, the reply to an install request, and the reply
// to a probe it is too busy to serve.
type State string

// The states of agent 2.1.6, as its own State::name reports them.
const (
	StateEntryPoint          State = "entry_point"
	StatePark                State = "park"
	StatePoll                State = "poll"
	StateProbe               State = "probe"
	StateValidation          State = "validation"
	StateDownload            State = "download"
	StateDirectDownload      State = "direct_download"
	StateInstall             State = "install"
	StateReboot              State = "reboot"
	StateError               State = "error"
	StatePrepareLocalInstall State = "prepare_local_install"
)

// ProbeOutcome discriminates the replies a probe can get.
type ProbeOutcome string

const (
	// ProbeUpdating reports that an update is available.
	ProbeUpdating ProbeOutcome = "updating"
	// ProbeNoUpdate reports that the server offers nothing new.
	ProbeNoUpdate ProbeOutcome = "no_update"
	// ProbeTryAgain reports that the server asked for a back-off.
	ProbeTryAgain ProbeOutcome = "try_again"
	// ProbeBusy reports that the agent did not probe at all, because it was in
	// a state that does not accept one. Any bare string other than the two
	// above is read as this outcome, so a state a later agent adds arrives as a
	// busy state rather than as a decode failure.
	ProbeBusy ProbeOutcome = "busy"
)

// ProbeResponse is the result of a probe.
//
// ProbeBusy is not a failure. The agent answers it, without reaching the
// server, whenever it is in a state that is not preemptive — and one of those
// states is named "error", which is why a caller must read Outcome rather than
// the state name alone.
type ProbeResponse struct {
	// Outcome is which of the four replies the agent sent.
	Outcome ProbeOutcome
	// TryAgainIn is the back-off the server asked for. It is set only when
	// Outcome is ProbeTryAgain. The agent states it in seconds.
	TryAgainIn time.Duration
	// BusyState is the state the agent was in when it refused the probe. It is
	// set only when Outcome is ProbeBusy.
	BusyState State
}

// StateResponse is the agent's answer to an install request. The agent replies
// with the state it was in when the request arrived, and refuses the request
// when that state cannot start an installation.
type StateResponse struct {
	// Accepted reports whether the agent took the request.
	Accepted bool
	// State is the state the agent was in when the request arrived.
	State State
}

// AbortDownloadResponse is the agent's answer to an abort request.
type AbortDownloadResponse struct {
	// Accepted reports whether there was a download to abort.
	Accepted bool
	// Message is the agent's own wording, for the accepted case and for the
	// refused one.
	Message string
}

// ErrCallOutstanding reports that a call gave up before it reached the agent,
// because an earlier call is still with the agent. It tells a caller that the
// agent is busy with its own earlier request, rather than slow to answer this
// one.
var ErrCallOutstanding = errors.New("updatehub: an earlier call is still with the agent")

// StatusError reports an HTTP status the agent's API is not documented to send
// for the call that got it. It carries the status code and the body, because a
// consumer must be able to tell "the agent answered 500" from "the reply did
// not decode".
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("updatehub: %s %s: unexpected status %d", e.Method, e.Path, e.StatusCode)
	}

	return fmt.Sprintf("updatehub: %s %s: unexpected status %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// Settings is the agent's configuration, as /info reports it.
type Settings struct {
	Firmware FirmwareSettings `json:"firmware"`
	Network  Network          `json:"network"`
	Polling  Polling          `json:"polling"`
	Storage  Storage          `json:"storage"`
	Update   Update           `json:"update"`
}

// FirmwareSettings names where the agent reads the firmware metadata.
type FirmwareSettings struct {
	Metadata string `json:"metadata"`
}

// Network is the agent's server address and the address its API listens on.
type Network struct {
	ServerAddress string `json:"server_address"`
	ListenSocket  string `json:"listen_socket"`
}

// Polling is the agent's automatic poll configuration. Interval carries the
// agent's own wording, such as "1h".
type Polling struct {
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

// Storage tells where the agent keeps its runtime settings.
type Storage struct {
	ReadOnly        bool   `json:"read_only"`
	RuntimeSettings string `json:"runtime_settings"`
}

// Update is the agent's download directory and the install modes it supports.
type Update struct {
	DownloadDir           string   `json:"download_dir"`
	SupportedInstallModes []string `json:"supported_install_modes"`
}

// MetadataStrings holds a device identity or attribute value. The agent writes
// a single value as a bare string and several as an array, so this type accepts
// both and always presents a slice.
type MetadataStrings []string

// UnmarshalJSON accepts a bare string as well as an array of strings.
func (m *MetadataStrings) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*m = MetadataStrings{single}
		return nil
	}

	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*m = many

	return nil
}

// MarshalJSON writes a single value as a bare string, as the agent does.
func (m MetadataStrings) MarshalJSON() ([]byte, error) {
	if len(m) == 1 {
		return json.Marshal(m[0])
	}

	return json.Marshal([]string(m))
}

// MetadataValue maps a device identity or attribute name to its values.
type MetadataValue map[string]MetadataStrings

// FirmwareMetadata is the metadata the agent loaded from the running firmware.
type FirmwareMetadata struct {
	ProductUID       string        `json:"product_uid"`
	Version          string        `json:"version"`
	Hardware         string        `json:"hardware"`
	PubKey           string        `json:"pub_key"`
	DeviceIdentity   MetadataValue `json:"device_identity"`
	DeviceAttributes MetadataValue `json:"device_attributes"`
}

// ServerAddress is the address the agent polls: the address a probe set, or
// empty for the configured default.
type ServerAddress string

// IsDefault reports whether the agent polls its configured address.
func (s ServerAddress) IsDefault() bool { return s == "" }

// UnmarshalJSON accepts both shapes the agent sends: the bare string "default",
// and {"custom": "<address>"}.
func (s *ServerAddress) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		if name != "default" {
			return fmt.Errorf("updatehub: unknown server address %q", name)
		}
		*s = ""

		return nil
	}

	var custom struct {
		Custom *string `json:"custom"`
	}
	if err := json.Unmarshal(data, &custom); err != nil {
		return err
	}
	if custom.Custom == nil {
		return fmt.Errorf("updatehub: unknown server address %s", data)
	}
	*s = ServerAddress(*custom.Custom)

	return nil
}

// MarshalJSON writes back the shape the agent sends.
func (s ServerAddress) MarshalJSON() ([]byte, error) {
	if s.IsDefault() {
		return json.Marshal("default")
	}

	return json.Marshal(struct {
		Custom string `json:"custom"`
	}{Custom: string(s)})
}

// RuntimePolling is what the agent remembers about its own polling.
type RuntimePolling struct {
	// Last is the timestamp of the last poll, in the agent's own wording.
	Last          string        `json:"last"`
	Retries       int           `json:"retries"`
	Now           bool          `json:"now"`
	ServerAddress ServerAddress `json:"server_address"`
}

// RuntimeUpdate is what the agent remembers about the update in flight.
type RuntimeUpdate struct {
	UpgradeToInstallation string `json:"upgrade_to_installation,omitempty"`
	AppliedPackageUID     string `json:"applied_package_uid,omitempty"`
}

// RuntimeSettings is the agent's persisted state.
type RuntimeSettings struct {
	Polling    RuntimePolling `json:"polling"`
	Update     RuntimeUpdate  `json:"update"`
	Path       string         `json:"path"`
	Persistent bool           `json:"persistent"`
}

// AgentInfo is the /info reply. Version is the agent's running version.
type AgentInfo struct {
	State           State            `json:"state"`
	Version         string           `json:"version"`
	Config          Settings         `json:"config"`
	Firmware        FirmwareMetadata `json:"firmware"`
	RuntimeSettings RuntimeSettings  `json:"runtime_settings"`
}

// LogEntry is one entry of the agent's in-memory log.
type LogEntry struct {
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Time    string            `json:"time"`
	Data    map[string]string `json:"data"`
}

// Log is the /log reply. FirstIndex is the absolute index of the first entry
// among all the entries the agent ever recorded, so a reader can resume from
// where it stopped.
type Log struct {
	Entries    []LogEntry `json:"entries"`
	FirstIndex int        `json:"first_index"`
}

// Client calls the UpdateHub agent's local HTTP API.
//
// A Client serialises its calls: one request reaches the agent at a time, and
// the others wait. This is not tidiness. Agent 2.1.6 panics a worker thread
// when several requests arrive together, and its API then answers nothing more
// until the agent restarts.
//
// The turn is held per agent rather than per Client, so a second Client built
// for the same base URL waits for the first one's call instead of arriving
// beside it. The turn is keyed on the base URL as given, so two spellings of
// one agent — localhost and 127.0.0.1 — are two turns, and a consumer that
// builds more than one Client should spell the address the same way.
//
// A context bounds the wait, never the request. When a caller gives up, the
// request keeps the turn until the agent answers it, until the connection
// breaks, or until the hold runs out, and only then does the next call start.
// The agent needs that: it hands each request to its state machine, and a
// request still in flight there widens the window in which the machine's own
// timer cancels the handler and panics the task waiting on it. Nothing on the
// wire says when the agent finishes, so the only safe reading of a call that
// gave up is that the agent is still busy — and a call that then gives up
// waiting for its turn reports ErrCallOutstanding, so a consumer can tell the
// two apart.
//
// A Client is safe for concurrent use.
type Client struct {
	baseURL string
	timeout time.Duration
	hold    time.Duration
	http    *http.Client

	// inFlight holds one token, and every Client for this agent holds the same
	// channel. Taking the token is the serialising lock, and a call waiting for
	// it still honours its context.
	inFlight chan struct{}
}

// agentTurns holds one token channel per agent base URL, so the serialising
// turn survives a consumer that builds a second Client. It never shrinks, and
// it is bounded by the number of distinct agent addresses a process talks to.
var agentTurns sync.Map

// NewClient returns a client for the agent at baseURL.
//
// baseURL must carry a scheme and a host; pass DefaultBaseURL for the ordinary
// case.
//
// timeout must be positive. It bounds any call whose context carries no
// deadline of its own, because an agent that stops answering must not be able
// to block its caller for ever.
//
// hold must be positive. It is how long the client keeps the agent's turn after
// its caller gave up, before it cancels the request and lets the next call
// through. Long is safer than short: the agent may still be working on the
// abandoned request, and the turn exists to keep a second one away from it. But
// an agent can also stay alive and answer nothing, and the hold is what
// guarantees the turn comes back from that.
func NewClient(baseURL string, timeout, hold time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("updatehub: timeout must be positive, got %v", timeout)
	}
	if hold <= 0 {
		return nil, fmt.Errorf("updatehub: hold must be positive, got %v", hold)
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("updatehub: parse base URL %q: %w", baseURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("updatehub: base URL %q needs a scheme and a host", baseURL)
	}

	address := strings.TrimSuffix(parsed.String(), "/")
	turn, _ := agentTurns.LoadOrStore(address, make(chan struct{}, 1))

	return &Client{
		baseURL:  address,
		timeout:  timeout,
		hold:     hold,
		http:     &http.Client{},
		inFlight: turn.(chan struct{}),
	}, nil
}

// Probe asks the agent to search the server for an update.
//
// Pass an empty customServer to probe the address the agent is configured with.
// The request then carries no body at all, which is what the agent's own SDKs
// send: an empty custom_server field makes agent 2.1.6 resolve the address to
// an empty string, answer 500, and keep answering 500 until it restarts.
func (c *Client) Probe(ctx context.Context, customServer string) (ProbeResponse, error) {
	var body any
	if customServer != "" {
		body = struct {
			CustomServer string `json:"custom_server"`
		}{CustomServer: customServer}
	}

	answer, err := c.do(ctx, http.MethodPost, "/probe", body)
	if err != nil {
		return ProbeResponse{}, err
	}
	if err := answer.expectOK(); err != nil {
		return ProbeResponse{}, err
	}

	return decodeProbeResponse(answer.body)
}

// GetInfo reads the agent's general information, including the version it runs.
func (c *Client) GetInfo(ctx context.Context) (*AgentInfo, error) {
	return getJSON[AgentInfo](ctx, c, "/info")
}

// GetLogs reads the agent's in-memory log entries.
func (c *Client) GetLogs(ctx context.Context) (*Log, error) {
	return getJSON[Log](ctx, c, "/log")
}

// getJSON runs the calls that read a JSON document from the agent.
func getJSON[T any](ctx context.Context, c *Client, path string) (*T, error) {
	answer, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if err := answer.expectOK(); err != nil {
		return nil, err
	}

	document := new(T)
	if err := decodeJSON(answer.body, document); err != nil {
		return nil, err
	}

	return document, nil
}

// RemoteInstall asks the agent to install the package at packageURL.
func (c *Client) RemoteInstall(ctx context.Context, packageURL string) (StateResponse, error) {
	body := struct {
		URL string `json:"url"`
	}{URL: packageURL}

	return c.requestInstall(ctx, "/remote_install", body)
}

// LocalInstall asks the agent to install the package already at filePath.
func (c *Client) LocalInstall(ctx context.Context, filePath string) (StateResponse, error) {
	body := struct {
		File string `json:"file"`
	}{File: filePath}

	return c.requestInstall(ctx, "/local_install", body)
}

// AbortDownload asks the agent to abort the download in flight.
func (c *Client) AbortDownload(ctx context.Context) (AbortDownloadResponse, error) {
	const path = "/update/download/abort"

	answer, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return AbortDownloadResponse{}, err
	}

	accepted, err := answer.accepted()
	if err != nil {
		return AbortDownloadResponse{}, err
	}

	if !accepted {
		var refused struct {
			Error string `json:"error"`
		}
		if err := decodeJSON(answer.body, &refused); err != nil {
			return AbortDownloadResponse{}, err
		}

		return AbortDownloadResponse{Message: refused.Error}, nil
	}

	var taken struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(answer.body, &taken); err != nil {
		return AbortDownloadResponse{}, err
	}

	return AbortDownloadResponse{Accepted: true, Message: taken.Message}, nil
}

// requestInstall runs the two install calls, which share a reply shape: the
// agent's state, at 200 when it took the request and at 406 when it refused.
func (c *Client) requestInstall(ctx context.Context, path string, body any) (StateResponse, error) {
	answer, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return StateResponse{}, err
	}

	accepted, err := answer.accepted()
	if err != nil {
		return StateResponse{}, err
	}

	var state State
	if err := decodeJSON(answer.body, &state); err != nil {
		return StateResponse{}, err
	}

	return StateResponse{Accepted: accepted, State: state}, nil
}

// reply is one answer from the agent, together with the call that asked for
// it, so a caller reads the status and builds an error without repeating the
// method and the path a third time.
type reply struct {
	method string
	path   string
	status int
	body   []byte
}

// accepted reports whether the agent took the request. The install and the
// abort calls answer 406 when the agent's state cannot serve them, which is a
// refusal to report rather than a failure.
func (r *reply) accepted() (bool, error) {
	switch r.status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotAcceptable:
		return false, nil
	default:
		return false, r.statusError()
	}
}

// expectOK returns an error for any status but 200.
func (r *reply) expectOK() error {
	if r.status == http.StatusOK {
		return nil
	}

	return r.statusError()
}

func (r *reply) statusError() error {
	return &StatusError{
		Method:     r.method,
		Path:       r.path,
		StatusCode: r.status,
		Body:       truncate(r.body),
	}
}

// do sends one request. It returns an error only when the exchange itself
// failed; every status the agent sends reaches the caller.
func (c *Client) do(ctx context.Context, method, path string, body any) (*reply, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("updatehub: encode %s request: %w", path, err)
		}
		payload = bytes.NewReader(encoded)
	}

	// The request outlives the caller's context on purpose, and c.hold is what
	// ends it when the agent answers neither the caller nor anyone after it.
	requestCtx, endRequest := context.WithCancel(context.WithoutCancel(ctx))

	request, err := http.NewRequestWithContext(requestCtx, method, c.baseURL+path, payload)
	if err != nil {
		endRequest()

		return nil, fmt.Errorf("updatehub: build %s request: %w", path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	select {
	case c.inFlight <- struct{}{}:
	case <-ctx.Done():
		endRequest()

		return nil, fmt.Errorf("updatehub: %s %s: %w: %w", method, path, ErrCallOutstanding, ctx.Err())
	}

	done := make(chan roundTripResult, 1)

	go func() {
		defer endRequest()
		defer func() { <-c.inFlight }()

		answer, err := c.roundTrip(request, method, path)
		done <- roundTripResult{answer: answer, err: err}
	}()

	select {
	case result := <-done:
		return result.answer, result.err
	case <-ctx.Done():
		time.AfterFunc(c.hold, endRequest)

		return nil, fmt.Errorf("updatehub: %s %s: %w", method, path, ctx.Err())
	}
}

// roundTripResult carries what an exchange produced back to the caller that
// may already have stopped waiting for it.
type roundTripResult struct {
	answer *reply
	err    error
}

// roundTrip runs one exchange to its end, even when the caller gave up.
func (c *Client) roundTrip(request *http.Request, method, path string) (*reply, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("updatehub: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("updatehub: read %s reply: %w", path, err)
	}
	if len(answer) > maxResponseBytes {
		return nil, fmt.Errorf("updatehub: %s %s: the reply is larger than %d bytes", method, path, maxResponseBytes)
	}

	return &reply{method: method, path: path, status: response.StatusCode, body: answer}, nil
}

func decodeJSON(body []byte, target any) error {
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("updatehub: decode reply %q: %w", truncate(body), err)
	}

	return nil
}

// decodeProbeResponse reads the four shapes a probe reply takes. Three are bare
// JSON strings and one is an object, because the agent serialises a Rust enum
// whose unit variants have no payload.
func decodeProbeResponse(body []byte) (ProbeResponse, error) {
	var name string
	if err := json.Unmarshal(body, &name); err == nil {
		switch ProbeOutcome(name) {
		case ProbeUpdating:
			return ProbeResponse{Outcome: ProbeUpdating}, nil
		case ProbeNoUpdate:
			return ProbeResponse{Outcome: ProbeNoUpdate}, nil
		default:
			return ProbeResponse{Outcome: ProbeBusy, BusyState: State(name)}, nil
		}
	}

	var delayed struct {
		TryAgain *int64 `json:"try_again"`
	}
	if err := decodeJSON(body, &delayed); err != nil {
		return ProbeResponse{}, err
	}
	if delayed.TryAgain == nil {
		return ProbeResponse{}, fmt.Errorf("updatehub: unknown probe reply %q", truncate(body))
	}

	return ProbeResponse{
		Outcome:    ProbeTryAgain,
		TryAgainIn: time.Duration(*delayed.TryAgain) * time.Second,
	}, nil
}

func truncate(body []byte) string {
	const limit = 256

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) <= limit {
		return string(trimmed)
	}

	return string(trimmed[:limit]) + "…"
}
