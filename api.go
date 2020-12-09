package updatehub

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/parnurzeal/gorequest"
)

type ProbeResponse interface{}

const (
	Updating = "updating"
	NoUpdate = "no_update"
	TryAgain = "try_again"
)

type APIState string

const (
	Park                = "park"
	EntryPoint          = "entry_point"
	Poll                = "poll"
	Validation          = "validation"
	Download            = "download"
	Install             = "install"
	Reboot              = "reboot"
	DirectDownload      = "direct_download"
	PrepareLocalInstall = "prepare_local_install"
	Error               = "error"
)

type Client struct {
}

type Metadata struct {
	Metadata string `json:"metadata"`
}

type Network struct {
	ServerAddress string `json:"server_address"`
	ListenSocket  string `json:"listen_socket"`
}

type Polling struct {
	Interval string `json:"interval"`
	Enabled  bool   `json:"enabled"`
}

type Storage struct {
	ReadOnly        bool   `json:"read_only"`
	RuntimeSettings string `json:"runtime_settings"`
}

type Update struct {
	DownloadDir           string   `json:"download_dir"`
	SupportedInstallModes []string `json:"supported_install_modes"`
}

type ServerAddress struct {
	Custom string `json:"custom"`
}

type DeviceAttributes struct {
	Attr1 string `json:"attr1"`
	Attr2 string `json:"attr2"`
}

type DeviceIdentity struct {
	ID1 string `json:"id1"`
	ID2 string `json:"id2"`
}

type Firmware struct {
	DeviceAttributes DeviceAttributes `json:"device_attributes"`
	DeviceIdentity   DeviceIdentity   `json:"device_identity"`
	Hardware         string           `json:"hardware"`
	PubKey           string           `json:"pub_key"`
	Version          string           `json:"version"`
}

type UpdatePackage struct {
	AppliedPackageUid      string `json:"applied_package_uid"`
	UpdgradeToInstallation string `json:"upgrade_to_installation"`
}

type Settings struct {
	Firmware Metadata `json:"firmware"`
	Network  Network  `json:"network"`
	Polling  Polling  `json:"polling"`
	Storage  Storage  `json:"storage"`
	Update   Update   `json:"update"`
}

type RuntimeSettings struct {
	Path       string        `json:"path"`
	Persistent bool          `json:"persistent"`
	Polling    PollingLog    `json:"polling"`
	Update     UpdatePackage `json:"update"`
}

type PollingLog struct {
	Last          string        `json:"last"`
	Now           bool          `json:"now"`
	Retries       int64         `json:"retries"`
	ServerAddress ServerAddress `json:"server_address"`
}

type AgentInfo struct {
	Config          Settings        `json:"config"`
	Firmware        Firmware        `json:"firmware"`
	RuntimeSettings RuntimeSettings `json:"runtime_settings"`
	State           APIState        `json:"state"`
	Version         string          `json:"version"`
}

type Entry struct {
	Data    interface{} `json:"data"`
	Level   string      `json:"level"`
	Message string      `json:"message"`
	Time    string      `json:"time"`
}

type Log struct {
	Entries []Entry `json:"entries"`
}

// NewClient instantiates a new updatehub agent client
func NewClient() *Client {
	return &Client{}
}

// Probe server address for update
func (c *Client) Probe(serverAddress string) (*ProbeResponse, error) {
	var probe ProbeResponse

	var req struct {
		ServerAddress string `json:"custom_server"`
	}
	req.ServerAddress = serverAddress

	response, err := processRequest(string("/probe"), &probe, req, "POST")
	return response.(*ProbeResponse), err
}

// GetInfo get updatehub agent general information
func (c *Client) GetInfo() (*AgentInfo, error) {
	response, err := processRequest(string("/info"), &AgentInfo{}, nil, "GET")
	return response.(*AgentInfo), err
}

// GetLogs get updatehub agent log entries
func (c *Client) GetLogs() (*Log, error) {
	response, err := processRequest(string("/log"), &Log{}, nil, "GET")
	return response.(*Log), err
}

// RemoteInstall trigger the installation of a package from a direct URL
func (c *Client) RemoteInstall(serverAddress string) (*APIState, error) {
	var state APIState

	var req struct {
		URL string `json:"url"`
	}
	req.URL = serverAddress

	response, err := processRequest(string("/remote_install"), &state, req, "POST")
	return response.(*APIState), err
}

// LocalInstall trigger the installation of a local package
func (c *Client) LocalInstall(filePath string) (*APIState, error) {
	var state APIState

	var req struct {
		FilePath string `json:"file"`
	}
	req.FilePath = filePath

	response, err := processRequest(string("/local_install"), &state, req, "POST")
	return response.(*APIState), err
}

func processRequest(url string, responseStruct interface{}, req interface{}, method string) (interface{}, error) {
	var body []byte
	var errs []error

	switch method {
	case "GET":
		_, body, errs = gorequest.New().Get(buildURL(url)).EndStruct(&responseStruct)
	case "POST":
		_, body, errs = gorequest.New().Post(buildURL(url)).Send(req).EndStruct(&responseStruct)
	}

	if len(errs) > 0 {
		return nil, errs[0]
	}

	err := json.Unmarshal([]byte(body), &responseStruct)
	if err != nil {
		return nil, err
	}

	return responseStruct, nil
}

func buildURL(path string) string {
	u, err := url.Parse("localhost:8080")
	if err != nil {
		panic(err)
	}

	return fmt.Sprintf("http://%s%s", u, path)
}
