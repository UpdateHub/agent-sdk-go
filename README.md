# updatehub agent SDK for Go

[![godoc](https://pkg.go.dev/badge/github.com/UpdateHub/agent-sdk-go/v2.svg)](https://pkg.go.dev/github.com/UpdateHub/agent-sdk-go/v2)

A Go client for the [UpdateHub](https://updatehub.io) agent's local API. It
speaks the agent's two transports:

- `Client` calls the HTTP API the agent serves on `localhost:8080`.
- `StateChange` serves the Unix socket the agent connects to before every state
  transition, so your program can veto one.

This release is validated against UpdateHub agent **2.1.6**.

## Install

```sh
go get github.com/UpdateHub/agent-sdk-go/v2
```

It needs Go 1.26 or later, and nothing outside the standard library.

## Ask the agent to search for an update

```go
client, err := updatehub.NewClient(updatehub.DefaultBaseURL, 30*time.Second, 5*time.Minute)
if err != nil {
	return err
}

response, err := client.Probe(ctx, "")
if err != nil {
	return err
}

switch response.Outcome {
case updatehub.ProbeUpdating:
	// an update is available
case updatehub.ProbeNoUpdate:
	// the server offers nothing new
case updatehub.ProbeTryAgain:
	// the server asked for a back-off of response.TryAgainIn
case updatehub.ProbeBusy:
	// the agent did not probe; it is in the response.BusyState state
}
```

Pass an empty custom server to probe the address the agent is configured with.
`ProbeBusy` is not a failure: the agent answers it, without reaching the server,
whenever it is in a state that does not accept a probe. One of those states is
named `error`, so read `Outcome` rather than the state name alone.

**One turn per agent.** A `Client` serialises its calls, because agent 2.1.6
panics a worker thread when several requests arrive together and then answers
nothing more until it restarts. The turn is held per agent rather than per
`Client`, so a second client built for the same base URL waits rather than
arriving beside the first — but the key is the base URL as written, so spell the
address the same way everywhere.

A context bounds the wait, never the request. When a caller gives up, the
request keeps the turn until the agent answers it, until the connection breaks,
or until the hold runs out — so a call that gave up cannot leave a second one
arriving beside it. A call that then gives up waiting for its turn reports
`ErrCallOutstanding`, which tells you the agent is busy with an earlier request
of yours rather than slow to answer this one.

The hold is the third argument to `NewClient`, and long is safer than short: the
agent may still be working on the abandoned request. It exists because an agent
can also stay alive and answer nothing, and something must guarantee the turn
comes back from that.

## Veto a state transition

```go
listener := updatehub.NewStateChange(updatehub.DefaultSocketPath)

listener.OnError(func(err error) {
	// one connection failed; the listener keeps serving
})

listener.OnState(updatehub.StateDownload, func(ctx context.Context, handler *updatehub.Handler) error {
	return handler.Cancel()
})

if err := listener.Bind(); err != nil {
	if errors.Is(err, syscall.EADDRINUSE) {
		// somebody else holds the socket, or a dead process left the path
	}

	return err
}
defer listener.Close()

return listener.Serve(ctx)
```

A connection that closes with nothing written lets the agent proceed, so an
unbound socket is not a degraded veto: it is consent. The same is true when the
agent's trigger script is missing, because the agent then never consults the
socket at all. `TriggerInstalled(updatehub.TriggerPath)` reports whether the script is
there; what to do about its absence is your program's decision, not this
package's.

## What changed in v2.0.0

The module path is now `github.com/UpdateHub/agent-sdk-go/v2`, and the API is
not compatible with what came before it. The rewrite is driven by one intended
consumer: a single-process firmware daemon on a device that has no way back if
the update channel breaks.

- **Nothing exits the process.** `log.Fatal` is gone from every error path.
- **No method panics.** Each one used to end in a single-value type assertion on
  a value that is nil on the error path, so an unreachable agent crashed the
  caller.
- **Every call reads the HTTP status code**, and reports an unexpected one as a
  `*StatusError` carrying the code and the body.
- **`Probe` sends no body when no custom server is given.** It used to send
  `{"custom_server": ""}`, which agent 2.1.6 answers with a 500 — and keeps
  answering with a 500 until it restarts.
- **`ProbeResponse` is a type**, not `interface{}`, and it carries all four
  replies the agent can send.
- **`LocalInstall`, `RemoteInstall` and `AbortDownload` report a refusal** as a
  result rather than as an error.
- **Calls to one agent are serialised**, through a turn held per base URL, and
  the turn lasts until the request really ends rather than until the caller
  stops waiting for it.
- **Configuration is a constructor parameter.** The base URL and the timeout go
  to `NewClient`, the socket path goes to `NewStateChange`, and the
  `UH_LISTENER_TEST` environment variable is deleted.
- **The listener binds without unlinking**, so a path that is already there
  fails with `EADDRINUSE` instead of being taken silently.
- **The listener has a lifetime.** `Bind`, `Serve(ctx)` and `Close` replace a
  `Listen` that blocked for ever, and it serves each connection in its own
  goroutine.
- **A callback takes a context and returns an error**, and errors reach the sink
  `OnError` registers. A callback that panics is reported there too, rather than
  taking down a process that could not have recovered it.
- **The package writes nothing to stdout and nothing through `log`.**
- **`gorequest` is gone**, with the `goproxy` and `pkg/errors` dependencies it
  pulled in. The package now needs the standard library alone.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
