package types

// CpakOptions is the struct that represents the options for the Cpak struct.
type CpakOptions struct {
	// BinPath is the path to the directory where the internal binaries
	// will be stored.
	BinPath string `json:"bin_path"`

	// ManifestsPath is the path to the directory where the manifests
	// will be stored.
	//
	// Note: manifests stored in this directory are not meant to be
	// used, just stored for future use and debug purposes.
	ManifestsPath string `json:"manifests_path"`

	// ExportsPath is the path to the directory where the exports
	// (binaries and desktop entries) will be stored.
	ExportsPath string `json:"exports_path"`

	// StorePath is the path to the directory where the images, containers,
	// states and the sqlite database will be stored.
	StorePath string `json:"store_path"`

	// CachePath is the path to the directory where the cache will be stored.
	//
	// Note: cache is intended to be used by the cpak pull function to store
	// the downloaded images and unpacked layers.
	CachePath string `json:"cache_path"`
}
