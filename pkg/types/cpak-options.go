package types

type CpakOptions struct {
	BinPath       string `json:"bin_path"`
	ManifestsPath string `json:"manifests_path"`
	ExportsPath   string `json:"exports_path"`
	StorePath     string `json:"store_path"`
	CachePath     string `json:"cache_path"`
	Mode          string `json:"mode"` // keep, drop (drop not fully implemented yet)
}
