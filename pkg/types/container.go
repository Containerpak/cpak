/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package types

import (
	"time"
)

// Container is the struct that represents a container in the store and
// in the cpak context.
type Container struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// CpakId is the unique identifier of the container, it is expected to be
	// unique across all the containers in the store.
	CpakId string `json:"cpak_id"`

	// ApplicationCpakId is the application the container is based on.
	ApplicationCpakId string `json:"application_cpak_id"`

	// Pid is the pid of the main spawned container process inside the namespace.
	Pid int `json:"pid"`

	// CreateTimestamp is the time the container was created in the store.
	CreateTimestamp time.Time `json:"create_timestamp"`

	// StatePath is the path to the state directory of the container, the
	// actual workdir for the layer mounts.
	StatePath string `json:"state_path"`

	// HostExecPid is the PID of the 'cpak hostexec-server' process running on the host for this container.
	HostExecPid int `json:"host_exec_pid"`

	// HostExecSocketPath is the path to the Unix domain socket used by the hostexec server/client.
	HostExecSocketPath string `json:"host_exec_socket_path"`

	// ExecSocketPath is the host path used to submit commands to the container init.
	ExecSocketPath string `json:"exec_socket_path"`

	// PolicyHash identifies the effective permissions used to create the namespaces.
	PolicyHash string `json:"policy_hash"`

	// Instance identifies the optional application instance.
	Instance string `json:"instance"`

	// LogPath is the host path where command output is retained.
	LogPath string `json:"log_path"`
}
