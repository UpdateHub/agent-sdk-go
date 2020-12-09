/*
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/
package main

import (
	"encoding/json"
	"fmt"
	"log"

	updatehub "github.com/UpdateHub/agent-sdk-go"
)

func main() {
	client := updatehub.NewClient()

	logs, err := client.GetLogs()
	if err != nil {
		log.Fatal(err)
	}

	resp, err := json.Marshal(logs)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")

	info, err := client.GetInfo()
	if err != nil {
		log.Fatal(err)
	}

	resp, err = json.Marshal(info)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")

	probe, err := client.Probe("")
	if err != nil {
		log.Fatal(err)
	}

	resp, err = json.Marshal(probe)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")

	probeCustom, err := client.Probe("http://www.example.com:8080")
	if err != nil {
		log.Fatal(err)
	}

	resp, err = json.Marshal(probeCustom)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")

	remoteInstall, err := client.RemoteInstall("https://foo.bar/update.uhu")
	if err != nil {
		log.Fatal(err)
	}

	resp, err = json.Marshal(remoteInstall)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")

	localInstall, err := client.LocalInstall("/tmp/update.uhu")
	if err != nil {
		log.Fatal(err)
	}

	resp, err = json.Marshal(localInstall)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(resp) + "\n")
}
