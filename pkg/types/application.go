package types

import (
	"time"

	"gorm.io/gorm"
)

type Application struct {
	gorm.Model
	// CpakId is the unique identifier of the application, it is expected to be
	// unique across all the applications in the store.
	CpakId string `gorm:"uniqueIndex;not null"`

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
	Origin string `gorm:"index"`

	// InstallTimestamp is the timestamp of the application creation in the store.
	InstallTimestamp time.Time

	// Binaries is the list of exported binaries of the application.
	Binaries string

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries string

	// Addons is the list of additional applications which it supports.
	Addons string

	// Layers is the list of layers of the application.
	Layers string

	// Config is the configuration of the application.
	Config string

	// Containers is the list of containers created for the application.
	Containers []Container `gorm:"foreignKey:ApplicationCpakId;references:CpakId"`

	// ParsedBinaries is the list of exported binaries of the application.
	ParsedBinaries []string `gorm:"-"`

	// ParsedDesktopEntries is the list of exported desktop entries of the application.
	ParsedDesktopEntries []string `gorm:"-"`

	// ParsedDependencies is the list of cpak dependencies needed by the application
	// to work properly.
	ParsedDependencies []Dependency `gorm:"-"`

	// ParsedAddons is the list of additional applications which it supports.
	ParsedAddons []string `gorm:"-"`

	// ParsedLayers is the list of layers of the application.
	ParsedLayers []string `gorm:"-"`

	// ParsedOverride is a set of permissions
	ParsedOverride Override `gorm:"-"`

	// Raw fields
	DependenciesRaw string
	OverrideRaw     string
}

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

type Dependency struct {
	Id      string
	Origin  string
	Branch  string
	Release string
	Commit  string
}
