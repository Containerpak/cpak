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

	// HostExecPid is retained to stop hostexec processes created by older cpak versions.
	HostExecPid int `json:"host_exec_pid"`

	// HostExecSocketPath is retained for stored containers created by older cpak versions.
	HostExecSocketPath string `json:"host_exec_socket_path"`

	// SystemBrokerPid is the PID of the policy-gated system integration broker.
	SystemBrokerPid int `json:"system_broker_pid"`

	// SystemBrokerSocketPath is the broker socket mounted into the container.
	SystemBrokerSocketPath string `json:"system_broker_socket_path"`

	// SystemBrokerTokenPath is a private capability file mounted read-only into the container.
	SystemBrokerTokenPath string `json:"system_broker_token_path"`

	// ExecSocketPath is the host path used to submit commands to the container init.
	ExecSocketPath string `json:"exec_socket_path"`

	// PolicyHash identifies the effective permissions used to create the namespaces.
	PolicyHash string `json:"policy_hash"`

	// CgroupPath is the delegated cgroup used for optional resource limits.
	CgroupPath string `json:"cgroup_path"`

	// Instance identifies the optional application instance.
	Instance string `json:"instance"`

	// LogPath is the host path where command output is retained.
	LogPath string `json:"log_path"`
}
