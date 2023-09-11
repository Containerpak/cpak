package types

import "time"

type Application struct {
	Id                 string
	Name               string
	Version            string
	Origin             string
	Timestamp          time.Time
	Binaries           []string
	DesktopEntries     []string
	FutureDependencies []string
	Layers             []string
	Config             string
}
