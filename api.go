package updatehub

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/parnurzeal/gorequest"
)

type Client struct {
}

type Settings struct {
	Firmware	Firmware
	Network		Network
	Polling		Polling
	Storage		Storage
	Update		Update
}

type Firmware struct {
	Metadata   string
}

type Network struct {
	ServerAddress	string
	ListenSocket	string
}

type Polling struct {
	Interval	string
	Enabled		bool
}

type Storage struct {
	ReadOnly			bool
	RunTimeSettings		string
}

type Update struct {
	DownloadDir				string
	SupportedInstallModes	string
}

type Response string 

const (
	ResponseUpdating		=	"updating"
	ResponseNoUpdate		=	"no_update"
	ResponseTryAgain		=	"try_again"
)

type APIState string

const (
	Park        			= "park"
	EntryPoint     			= "entry_point"
	Poll       				= "poll"
	Validation 				= "validation"
	Download  				= "download"
	Install  				= "install"
	Reboot   				= "reboot"
	DirectDownload			= "direct_download"
	PrepareLocalInstall		= "prepare_local_install"
	Error       			= "error"
)

type AgentInfo struct {
	State    			StateID					   	`json:"state"`
	Version  			string                    	`json:"version"`
	Config   			Settings        			`json:"config"`
	Firmware 			Firmware  					`json:"firmware"`
	RuntimeSettingsPath string						`json:"runtime-settings-path"`
}

type Log struct {
	Entries		[]Entry		`json:"entries"`
}

type Entry struct {
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
	return c.probe("")
}

// ProbeCustomServer probe custom server address for update
func (c *Client) ProbeCustomServer(serverAddress string) (*ProbeResponse, error) {
	return c.probe(serverAddress)
}

func (c *Client) probe(serverAddress string) (*ProbeResponse, error) {
	var probe ProbeResponse

	var req struct {
		ServerAddress   string `json:"server-address"`
	}
	req.ServerAddress = serverAddress

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
func (c *Client) GetLogs() (*Log, error) {
	_, body, errs := gorequest.New().Get(buildURL("/log")).End()
	if len(errs) > 0 {
		return nil, errs[0]
	}

	var entries []Entry
	var log Log

	err := json.Unmarshal([]byte(body), &entries)
	if err != nil {
		return nil, err
	}
	log.Entries = entries

	return &log, nil
}

// RemoteInstall trigger the installation of a package from a direct URL
func (c *Client) RemoteInstall(serverAddress string) (*StateID, error) {
	var state StateID

	var req struct {
		ServerAddress   string `json:"server-address"`
	}
	req.ServerAddress = serverAddress

	_, _, errs := gorequest.New().Post(buildURL("/remote_install")).Send(req).EndStruct(&state)
	if len(errs) > 0 {
		return nil, errs[0]
	}

	return &state, nil
}

// LocalInstall trigger the installation of a local package
func (c *Client) LocalInstall(filePath string) (*StateID, error) {
	var state StateID

	var req struct {
		FilePath 	string	`json:"file-path"`
	}
	req.FilePath = filePath
	
	_, _, errs := gorequest.New().Post(buildURL("/local_install")).Send(req).EndStruct(&state)
	if len(errs) > 0 {
		return nil, errs[0]
	}

	return &state, nil
}

func buildURL(path string) string {
	u, err := url.Parse("localhost:8080")
	if err != nil {
		panic(err)
	}

	return fmt.Sprintf("http://%s/%s", u.Host, path[1:])
}
