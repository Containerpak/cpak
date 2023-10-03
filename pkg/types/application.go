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

	// Followings are the remote (branch, release, commit) that the application
	// was installed from.
	Branch  string
	Release string
	Commit  string

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

	// Dependencies is the list of cpak dependencies needed by the application
	// to work properly.
	Dependencies []Dependency

	// Addons is the list of additional applications which it supports.
	Addons []string

	// Layers is the list of layers of the application.
	Layers []string

	// Config is the configuration of the application.
	Config string

	// Override is a set of permissions
	Override Override
}

// SourceType returns the type of the application's source.
func (a Application) SourceType() string {
	switch {
	case a.Branch != "":
		return "branch"
	case a.Release != "":
		return "release"
	case a.Commit != "":
		return "commit"
	}
	return "unknown"
}

// Dependency is the struct that represents a dependency of an application.
type Dependency struct {
	Id      string
	Origin  string
	Branch  string
	Release string
	Commit  string
}
