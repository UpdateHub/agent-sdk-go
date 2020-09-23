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
	l := updatehub.NewStateChangeListener()

	l.On(updatehub.StateDownload, func(state *updatehub.State) {
		fmt.Println("downloading state")
		fmt.Println("Canceling the command...")
		state.Cancel()
		fmt.Println("Done")
	})

	l.On(updatehub.StateError, func(state *updatehub.State) {
		fmt.Println("Error")
		state.Proceed()
		fmt.Println("Done")
	})

	l.On(updatehub.StateReboot, func(state *updatehub.State) {
		fmt.Println("rebooting...")
	})

	err := l.Listen()
	if err != nil {
		log.Fatal(err)
	}
}
