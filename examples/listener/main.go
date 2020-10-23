/*
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/
package main

import (
	"fmt"
	"log"

	updatehub "github.com/UpdateHub/agent-sdk-go"
)

func main() {
	listener := updatehub.NewStateChange()

	listener.OnState(updatehub.StateDownload, func(handler *updatehub.Handler) {
		fmt.Println("function called when starting the Download state; it will cancel the transition")
		handler.Cancel()
	})

	listener.OnState(updatehub.StateInstall, func(handler *updatehub.Handler) {
		fmt.Println("function called when starting the Install state")
		handler.Proceed()
	})

	err := listener.Listen()
	if err != nil {
		log.Fatal(err)
	}
}
