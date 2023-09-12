package types

import "time"

// Container is the struct that represents a container in the store and
// in the cpak context.
type Container struct {
	// Id is the unique identifier of the container, it is expected to be
	// unique across all the containers in the store.
	Id string

	// Application is the application the container is based on.
	Application Application

	// Timestamp is the time the container was created in the store.
	Timestamp time.Time

	// RootFs is the path to the root filesystem of the container.
	RootFs string

	// Pid is the pid of the spawned container process.
	Pid int

	// StatePath is the path to the state directory of the container, the
	// actual workdir for the layer mounts.
	StatePath string
}
