package types

import "time"

type Container struct {
	Id          string
	Application Application
	Timestamp   time.Time
	RootFs      string
	Pid         int
	StatePath   string
}
