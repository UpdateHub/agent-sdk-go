/*
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	updatehub "github.com/UpdateHub/agent-sdk-go/v2"
)

func main() {
	socketPath := updatehub.DefaultSocketPath
	if len(os.Args) > 1 {
		socketPath = os.Args[1]
	}

	installed, err := updatehub.TriggerInstalled(updatehub.TriggerPath)
	if err != nil {
		log.Fatal(err)
	}
	if !installed {
		fmt.Println("the trigger script is missing; the agent will not consult this socket")
	}

	listener := updatehub.NewStateChange(socketPath)

	listener.OnError(func(err error) {
		fmt.Println("state change error:", err)
	})

	listener.OnState(updatehub.StateDownload, func(_ context.Context, handler *updatehub.Handler) error {
		fmt.Println("the agent is about to download; cancelling the transition")

		return handler.Cancel()
	})

	listener.OnState(updatehub.StateInstall, func(_ context.Context, handler *updatehub.Handler) error {
		fmt.Println("the agent is about to install; letting it proceed")

		return handler.Proceed()
	})

	if err := listener.Bind(); err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			fmt.Println("another process holds", socketPath)
		}

		log.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := listener.Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
