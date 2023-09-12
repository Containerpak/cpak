package types

import "time"

// Application is the struct that represents an application in the store
// and in the cpak context.
type Application struct {
	// Id is the unique identifier of the application, it is expected to be
	// unique across all the applications in the store.
	Id string

	// Name is the name of the application.
	Name string

	// Version is the version of the application. It is expected to be unique
	// for each application's origin.
	// Note: the version is not required to be in a specific format. Currently
	// there are no checks for its uniqueness.
	Version string

	// Origin is the origin of the application. It is expected to be unique
	// for each application's version, and should be a git repository URL
	// without the protocol and the trailing .git.
	Origin string

	// Timestamp is the timestamp of the application creation in the store.
	Timestamp time.Time

	// Binaries is the list of exported binaries of the application.
	Binaries []string

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries []string

	// FutureDependencies is the list of future dependencies of the application.
	FutureDependencies []string

	// Layers is the list of layers of the application.
	Layers []string

	// Config is the configuration of the application.
	Config string
}
