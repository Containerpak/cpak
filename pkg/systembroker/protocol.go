/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import "encoding/json"

const (
	ProtocolVersion = 2

	ActionNotify            = "desktop.notify"
	ActionOpenURI           = "desktop.open-uri"
	ActionLaunchApplication = "desktop.launch-application"
	ActionContainers        = "containers"

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
	URI string `json:"uri"`
}

type LaunchApplicationRequest struct {
	ApplicationToken string            `json:"application_token"`
	URIs             []string          `json:"uris,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
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
