/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"time"
)

type Application struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// CpakId is the unique identifier of the application, it is expected to be
	// unique across all the applications in the store.
	CpakId string `json:"cpak_id"`

	// Name is the name of the application.
	Name string `json:"name"`

	// Version is the version of the application. It is expected to be unique
	// for each application's origin.
	// Note: the version is not required to be in a specific format. Currently
	// there are no checks for its uniqueness.
	Version string `json:"version"`

	// Followings are the remote (branch, release, commit) that the application
	// was installed from.
	Branch  string `json:"branch"`
	Release string `json:"release"`
	Commit  string `json:"commit"`

	// Origin is the origin of the application. It is expected to be unique
	// for each application's version, and should be a git repository URL
	// without the protocol and the trailing .git.
	Origin string `json:"origin"`

	// InstallTimestamp is the timestamp of the application creation in the store.
	InstallTimestamp time.Time `json:"install_timestamp"`

	// Binaries is the list of exported binaries of the application.
	Binaries string `json:"binaries"`

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries string `json:"desktop_entries"`

	// Addons is the list of additional applications which it supports.
	Addons string `json:"addons"`

	// Layers is the list of layers of the application.
	Layers string `json:"layers"`

	// Config is the configuration of the application.
	Config string `json:"config"`

	// Image is the OCI reference declared by the manifest.
	Image string `json:"image"`

	// ImageDigest is the resolved immutable OCI image digest.
	ImageDigest string `json:"image_digest"`

	// ManifestDigest names the manifest this installation was made from, as it
	// stood once cpak had validated and migrated it. It is absent from every
	// record written before it was introduced, and for those the manifest is
	// not part of what the application is recognised by.
	ManifestDigest string `json:"manifest_digest,omitempty"`

	// Containers is the list of containers created for the application.
	Containers []Container `json:"containers"`

	// ParsedBinaries is the list of exported binaries of the application.
	ParsedBinaries []string `json:"parsed_binaries"`

	// ParsedServices are named persistent application commands.
	ParsedServices map[string]ApplicationService `json:"parsed_services,omitempty"`

	// ParsedDesktopEntries is the list of exported desktop entries of the application.
	ParsedDesktopEntries []string `json:"parsed_desktop_entries"`

	// ParsedSessions is the list of login sessions exported by the application.
	ParsedSessions []Session `json:"parsed_sessions"`

	// ParsedDependencies is the list of cpak dependencies needed by the application
	// to work properly.
	ParsedDependencies []Dependency `json:"parsed_dependencies"`

	// ParsedAddons is the list of additional applications which it supports.
	ParsedAddons []string `json:"parsed_addons"`

	// ParsedAddonProvider is the addon capability supplied by this application.
	ParsedAddonProvider *AddonProvider `json:"parsed_addon_provider,omitempty"`

	// IdleTime is the number of idle minutes before the container stops.
	IdleTime int `json:"idle_time"`

	// ParsedLayers is the list of layers of the application.
	ParsedLayers []string `json:"parsed_layers"`

	RuntimeSources []RuntimeSource `json:"runtime_sources"`

	// ParsedOverride is a set of permissions
	ParsedOverride Override `json:"parsed_override"`

	// PulledIn records that this installation exists because another package
	// declared it, and not because the user named it. What the user agreed to
	// is the package that pulled this one in, so it gets no launchers of its
	// own and, when it is started directly, it is held to what that package
	// may do.
	//
	// It is absent from every record written before it was introduced, and
	// those keep the answer they had: an installation cpak cannot tell apart
	// from one the user named is treated as one the user named, because
	// narrowing an application somebody has been launching for months, on a
	// guess, is the worse of the two mistakes.
	//
	// Naming the same origin in an install of its own clears it: the user has
	// then asked for the package in their own right, which is the whole of
	// what this field records.
	PulledIn bool `json:"pulled_in,omitempty"`

	// PulledInBy is the origin of the installation that brought this one here,
	// and it is the only one with a say in how this one starts on its own. Any
	// package may declare any origin as a dependency; declaring one is not the
	// same as having been the reason it is installed, and without this a
	// publisher could narrow an installation they had nothing to do with by
	// naming it.
	//
	// It is empty on a record the user named, and empty on a record written
	// before it existed. In the second case cpak does not know which package
	// brought the installation here, and every package that declares it is
	// taken to have a say, which is the narrower of the two answers.
	PulledInBy string `json:"pulled_in_by,omitempty"`

	// Raw fields
	DependenciesRaw string `json:"dependencies_raw"`
	OverrideRaw     string `json:"override_raw"`
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
	Id      string `json:"id,omitempty" jsonschema:"description=Installed dependency identifier"`
	Origin  string `json:"origin" jsonschema:"minLength=1,description=Dependency repository origin"`
	Branch  string `json:"branch,omitempty" jsonschema:"description=Dependency branch"`
	Release string `json:"release,omitempty" jsonschema:"description=Dependency release"`
	Commit  string `json:"commit,omitempty" jsonschema:"description=Dependency commit"`
	Mode    string `json:"mode,omitempty" jsonschema:"enum=nested,enum=layer,description=Dependency integration mode"`
}

func (d Dependency) IsLayer() bool {
	return d.Mode == "layer"
}

func (d Dependency) IsNested() bool {
	return d.Mode == "" || d.Mode == "nested"
}

// UpdateStatus is the outcome of an update attempt on a single application.
type UpdateStatus string

const (
	UpdateStatusUpdated  UpdateStatus = "updated"
	UpdateStatusUpToDate UpdateStatus = "up-to-date"

	// UpdateStatusPinned is reported for applications bound to an immutable
	// commit, which are never moved.
	UpdateStatusPinned UpdateStatus = "pinned"

	// UpdateStatusUnsupported is reported when the target version cannot be
	// resolved for the repository host.
	UpdateStatusUnsupported UpdateStatus = "unsupported"

	// UpdateStatusFailed is reported when the update did not complete and the
	// previous installation was preserved.
	UpdateStatusFailed UpdateStatus = "failed"

	// UpdateStatusPermissionDenied is reported when the update requests new
	// permissions that were not explicitly approved.
	UpdateStatusPermissionDenied UpdateStatus = "permission-denied"
)

// UpdateResult describes what happened to a single application during an
// update, it is meant to be formatted by the caller.
type UpdateResult struct {
	Origin              string       `json:"origin"`
	Name                string       `json:"name"`
	SourceType          string       `json:"source_type"`
	Status              UpdateStatus `json:"status"`
	OldVersion          string       `json:"old_version"`
	NewVersion          string       `json:"new_version"`
	PermissionChanges   []string     `json:"permission_changes,omitempty"`
	PermissionAdditions []string     `json:"permission_additions,omitempty"`
	Reason              string       `json:"reason,omitempty"`
	Err                 error        `json:"-"`
}
