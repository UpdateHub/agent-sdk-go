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

	"github.com/UpdateHub/agent-sdk-go"
)

func main() {
	client := updatehub.NewClient()

	info, err := client.GetInfo()
	if err != nil {
		log.Fatal(err)
	}

	infoResponse, err := json.Marshal(info)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(infoResponse))

	probe, err := client.Probe("")
	if err != nil {
		log.Fatal(err)
	}

	probeResponse, err := json.Marshal(probe)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(probeResponse))

	probeCustom, err := client.Probe("http://www.example.com:8080")
	if err != nil {
		log.Fatal(err)
	}

	probeCustomResponse, err := json.Marshal(probeCustom)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(probeCustomResponse))

	logs, err := client.GetLogs()
	if err != nil {
		log.Fatal(err)
	}

	logResponse, err := json.Marshal(logs)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(logResponse))

	remoteInstall, err := client.RemoteInstall("https://foo.bar/update.uhu")
	if err != nil {
		log.Fatal(err)
	}

	remoteInstallResponse, err := json.Marshal(remoteInstall)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(remoteInstallResponse))

	localInstall, err := client.LocalInstall("/tmp/update.uhu")

	if err != nil {
		log.Fatal(err)
	}

	localInstallResponse, err := json.Marshal(localInstall)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(string(localInstallResponse))
}
