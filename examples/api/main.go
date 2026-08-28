/*
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	updatehub "github.com/UpdateHub/agent-sdk-go/v2"
)

func main() {
	// The hold is how long the client keeps the agent's turn after a call gives
	// up, so that the next call cannot arrive beside a request the agent may
	// still be working on.
	client, err := updatehub.NewClient(updatehub.DefaultBaseURL, 30*time.Second, 5*time.Minute)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	logs, err := client.GetLogs(ctx)
	if err != nil {
		log.Fatal(err)
	}
	dump(logs)

	info, err := client.GetInfo(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("agent version:", info.Version)

	// An empty custom server probes the address the agent is configured with.
	probe, err := client.Probe(ctx, "")
	if err != nil {
		log.Fatal(err)
	}

	switch probe.Outcome {
	case updatehub.ProbeUpdating:
		fmt.Println("an update is available")
	case updatehub.ProbeNoUpdate:
		fmt.Println("no update is available")
	case updatehub.ProbeTryAgain:
		fmt.Println("the server asked for a back-off of", probe.TryAgainIn)
	case updatehub.ProbeBusy:
		fmt.Println("the agent did not probe; it is in the", probe.BusyState, "state")
	}

	remoteInstall, err := client.RemoteInstall(ctx, "https://foo.bar/update.uhupkg")
	if err != nil {
		log.Fatal(err)
	}
	dump(remoteInstall)

	localInstall, err := client.LocalInstall(ctx, "/tmp/update.uhupkg")
	if err != nil {
		log.Fatal(err)
	}
	dump(localInstall)

	abortDownload, err := client.AbortDownload(ctx)
	if err != nil {
		log.Fatal(err)
	}
	dump(abortDownload)
}

func dump(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(encoded))
}
