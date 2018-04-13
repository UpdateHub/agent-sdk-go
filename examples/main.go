package main

import (
	"fmt"
	"log"

	"github.com/updatehub/agent-sdk-go"
)

func main() {
	l := updatehub.NewStateChangeListener()
	l.On(updatehub.ActionEnter, updatehub.StateDownloading, func(action updatehub.Action, state *updatehub.State) {
		if action == updatehub.ActionEnter && state.ID == updatehub.StateDownloading {
			fmt.Println("enter downloading state")
		}

		state.Cancel()
	})

	l.OnError(func(error string) {
		fmt.Println(error)
	})

	err := l.Listen()
	if err != nil {
		log.Fatal(err)
	}
}
