/*
Copyright (C) 2019
O.S. Systems Sofware LTDA: contato@ossystems.com.br

SPDX-License-Identifier: Apache-2.0
*/
package main

import (
	"fmt"

	"github.com/UpdateHub/agent-sdk-go"
)

func main() {
	client := updatehub.NewClient()

	info, err:= client.GetInfo()
	probe, err:= client.Probe()
	probeCustom, err:= client.ProbeCustomServer("http://www.example.com:8080")
	logs, err:= client.GetLogs()
	remoteInstall, err:= client.RemoteInstall("https://foo.bar/update.uhu")
	localInstall, err:= client.LocalInstall("/tmp/update.uhu")
	
	if err != nil {
		fmt.Println(info)
		fmt.Println(probe)
		fmt.Println(probeCustom)
		fmt.Println(logs)
		fmt.Println(remoteInstall)
		fmt.Println(localInstall)
	}
}
