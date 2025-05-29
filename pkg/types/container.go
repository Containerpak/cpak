/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

import (
	"time"

	"gorm.io/gorm"
)

// Container is the struct that represents a container in the store and
// in the cpak context.
type Container struct {
	gorm.Model
	// CpakId is the unique identifier of the container, it is expected to be
	// unique across all the containers in the store.
	CpakId string `gorm:"uniqueIndex;not null"`

	// ApplicationCpakId is the application the container is based on.
	ApplicationCpakId string `gorm:"index;not null"`

	// Pid is the pid of the main spawned container process inside the namespace.
	Pid int

	// CreateTimestamp is the time the container was created in the store.
	CreateTimestamp time.Time

	// StatePath is the path to the state directory of the container, the
	// actual workdir for the layer mounts.
	StatePath string

	// HostExecPid is the PID of the 'cpak hostexec-server' process running on the host for this container.
	HostExecPid int

	// HostExecSocketPath is the path to the Unix domain socket used by the hostexec server/client.
	HostExecSocketPath string
}
