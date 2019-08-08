package updatehub

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/UpdateHub/updatehub/metadata"
	"github.com/UpdateHub/updatehub/updatehub"
	"github.com/parnurzeal/gorequest"
)

var DefaultHost = updatehub.DefaultSettings.ListenSocket

type Client struct {
}

type AgentInfo struct {
	Version  string                    `json:"version"`
	Config   updatehub.Settings        `json:"config"`
	Firmware metadata.FirmwareMetadata `json:"firmware"`
}

type LogEntry struct {
	Data    interface{} `json:"data"`
	Level   string      `json:"level"`
	Message string      `json:"message"`
	Time    string      `json:"time"`
}

type ProbeResponse struct {
	UpdateAvailable bool `json:"update-available"`
	TryAgainIn      int  `json:"try-again-in"`
}

// NewClient instantiates a new updatehub agent client
func NewClient() *Client {
	return &Client{}
}

// Probe default server address for update
func (c *Client) Probe() (*ProbeResponse, error) {
	return c.probe("", false)
}

// ProbeCustomServer probe custom server address for update
func (c *Client) ProbeCustomServer(serverAddress string, ignoreProbeASAP bool) (*ProbeResponse, error) {
	return c.probe(serverAddress, ignoreProbeASAP)
}

func (c *Client) probe(serverAddress string, ignoreProbeASAP bool) (*ProbeResponse, error) {
	var probe ProbeResponse

	var req struct {
		ServerAddress   string `json:"server-address"`
		IgnoreProbeASAP bool   `json:"ignore-probe-asap"`
	}
	req.ServerAddress = serverAddress
	req.IgnoreProbeASAP = ignoreProbeASAP

	_, _, errs := gorequest.New().Post(buildURL("/probe")).Send(req).EndStruct(&probe)
	if len(errs) > 0 {
		return nil, errs[0]
	}

	return &probe, nil
}

// GetInfo get updatehub agent general information
func (c *Client) GetInfo() (*AgentInfo, error) {
	var info AgentInfo

	_, _, errs := gorequest.New().Get(buildURL("/info")).EndStruct(&info)
	if len(errs) > 0 {
		return nil, errs[0]
	}

	return &info, nil
}

// GetLogs get updatehub agent log entries
func (c *Client) GetLogs() ([]LogEntry, error) {
	_, body, errs := gorequest.New().Get(buildURL("/log")).End()
	if len(errs) > 0 {
		return nil, errs[0]
	}

	var entries []LogEntry

	err := json.Unmarshal([]byte(body), &entries)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func buildURL(path string) string {
	u, err := url.Parse(DefaultHost)
	if err != nil {
		panic(err)
	}

	return fmt.Sprintf("http://%s/%s", u.Host, path[1:])
}
