package types

type Manifest struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Version            string   `json:"version"`
	Image              string   `json:"image"`
	Binaries           []string `json:"binaries"`
	DesktopEntries     []string `json:"desktop_entries"`     // non mandatory
	Dependencies       []string `json:"dependencies"`        // non mandatory
	FutureDependencies []string `json:"future_dependencies"` // non mandatory, dependencies that could be exported in the future
	IdleTime           int      `json:"idle_time"`           // non mandatory, idle time in minutes, after which to destroy the container
}
