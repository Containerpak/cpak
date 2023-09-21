package types

// CpakManifest is the struct that represents the manifest of an application.
type CpakManifest struct {
	// Name is the name of the application.
	Name string `json:"name"`

	// Description is the description of the application. It is expected to be
	// as concise as possible.
	Description string `json:"description"`

	// Version is the version of the application in the cpak context. It is
	// not required to be in a specific format.
	//
	// Note: the application version can be different from the version of the
	// origin repository. It doesn't matter which version the image is using,
	// this version is the one cpak will use and display to the user. It is
	// the responsibility of whoever packages the application to respect the
	// software developer's versioning.
	Version string `json:"version"`

	// Image is the image of the application. It is expected to be a valid
	// OCI image (full image reference).
	Image string `json:"image"`

	// Binaries is the list of exported binaries of the application.
	Binaries []string `json:"binaries"`

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries []string `json:"desktop_entries"`

	// Dependencies is the list of dependencies of the application, it is
	// expected to be a list of origin repositories.
	//
	// Note: versions are not supported yet.
	Dependencies []string `json:"dependencies"`

	// FutureDependencies is the list of future dependencies of the application.
	// These are dependencies that could be exported in the future and that
	// are not required at the moment. For example, a game engine could specify
	// a future dependency for one or more IDEs, so that when the user installs
	// one of these IDEs, the game engine will automatically see it and use it.
	FutureDependencies []string `json:"future_dependencies"`

	// IdleTime is the idle time in minutes, after which to destroy the
	// container.
	//
	// Note: this is not used yet.
	IdleTime int `json:"idle_time"` // non mandatory, idle time in minutes, after which to destroy the container

	// Override is a set of permissions that the user can grant to the
	// application, even if this is called "override", it is also used to
	// set the default permissions.
	Override Override `json:"override"`
}
