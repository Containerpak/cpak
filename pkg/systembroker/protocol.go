/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import "encoding/json"

const (
	ProtocolVersion = 2

	ActionNotify            = "desktop.notify"
	ActionOpenURI           = "desktop.open-uri"
	ActionLaunchApplication = "desktop.launch-application"
	ActionFilePicker        = "desktop.file-picker"
	ActionContainers        = "containers"
	ActionCpak              = "cpak"

	FrameStdout = "stdout"
	FrameStderr = "stderr"
	FrameExit   = "exit"
	FrameError  = "error"
)

type Request struct {
	Version int             `json:"version"`
	Token   string          `json:"token"`
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

type Frame struct {
	Type     string `json:"type"`
	Data     []byte `json:"data,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type NotificationRequest struct {
	AppName       string `json:"app_name"`
	ReplaceID     uint32 `json:"replace_id,omitempty"`
	Icon          string `json:"icon,omitempty"`
	Summary       string `json:"summary"`
	Body          string `json:"body,omitempty"`
	Urgency       string `json:"urgency,omitempty"`
	Category      string `json:"category,omitempty"`
	ExpireTimeout int32  `json:"expire_timeout"`
	Transient     bool   `json:"transient,omitempty"`
}

type OpenURIRequest struct {
	URI             string `json:"uri"`
	ActivationToken string `json:"activation_token,omitempty"`
}

type LaunchApplicationRequest struct {
	ApplicationToken string            `json:"application_token"`
	URIs             []string          `json:"uris,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
}

type FilePickerPolicy struct {
	OpenFile         bool `json:"open_file,omitempty"`
	OpenFolder       bool `json:"open_folder,omitempty"`
	SaveFile         bool `json:"save_file,omitempty"`
	Persistent       bool `json:"persistent,omitempty"`
	ContainingFolder bool `json:"containing_folder,omitempty"`
}

type FilePickerPathGrant struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

func (p FilePickerPolicy) Enabled() bool {
	return p.OpenFile || p.OpenFolder || p.SaveFile
}

type FilePickerRequest struct {
	Mode          string             `json:"mode"`
	ParentWindow  string             `json:"parent_window,omitempty"`
	Title         string             `json:"title"`
	AcceptLabel   string             `json:"accept_label,omitempty"`
	SuggestedName string             `json:"suggested_name,omitempty"`
	CurrentFolder string             `json:"current_folder,omitempty"`
	Multiple      bool               `json:"multiple,omitempty"`
	Filters       []FilePickerFilter `json:"filters,omitempty"`
}

type FilePickerFilter struct {
	Name      string   `json:"name"`
	Patterns  []string `json:"patterns,omitempty"`
	MIMETypes []string `json:"mime_types,omitempty"`
}

type FilePickerResult struct {
	Path             string   `json:"path"`
	Paths            []string `json:"paths,omitempty"`
	Kind             string   `json:"kind"`
	Access           string   `json:"access"`
	Lifetime         string   `json:"lifetime"`
	ContainingFolder bool     `json:"containing_folder,omitempty"`
}

type ContainerRequest struct {
	Backend     string           `json:"backend,omitempty"`
	Operation   string           `json:"operation"`
	Resources   []string         `json:"resources,omitempty"`
	Command     []string         `json:"command,omitempty"`
	Image       string           `json:"image,omitempty"`
	Name        string           `json:"name,omitempty"`
	Environment []string         `json:"environment,omitempty"`
	Mounts      []ContainerMount `json:"mounts,omitempty"`
	Ports       []string         `json:"ports,omitempty"`
	Workdir     string           `json:"workdir,omitempty"`
	User        string           `json:"user,omitempty"`
	Entrypoint  string           `json:"entrypoint,omitempty"`
	All         bool             `json:"all,omitempty"`
	Follow      bool             `json:"follow,omitempty"`
	Remove      bool             `json:"remove,omitempty"`
	Force       bool             `json:"force,omitempty"`
	Detach      bool             `json:"detach,omitempty"`
	NoTrunc     bool             `json:"no_trunc,omitempty"`
	NoStream    bool             `json:"no_stream,omitempty"`
	Timestamps  bool             `json:"timestamps,omitempty"`
	Tail        *int             `json:"tail,omitempty"`
	Format      string           `json:"format,omitempty"`
	Timeout     int              `json:"timeout,omitempty"`
}

type ContainerMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type ContainerPathGrant struct {
	Path     string
	ReadOnly bool
}

type CpakRequest struct {
	Arguments   []string `json:"arguments"`
	Interactive bool     `json:"interactive,omitempty"`
	Rows        uint16   `json:"rows,omitempty"`
	Columns     uint16   `json:"columns,omitempty"`
}
