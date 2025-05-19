package types

type Override struct {
	SocketX11        bool `json:"socketX11" flag:"socketX11,bool"`
	SocketWayland    bool `json:"socketWayland" flag:"socketWayland,bool"`
	SocketPulseAudio bool `json:"socketPulseAudio" flag:"socketPulseAudio,bool"`
	SocketSessionBus bool `json:"socketSessionBus" flag:"socketSessionBus,bool"`
	SocketSystemBus  bool `json:"socketSystemBus" flag:"socketSystemBus,bool"`
	SocketSshAgent   bool `json:"socketSshAgent" flag:"socketSshAgent,bool"`
	SocketCups       bool `json:"socketCups" flag:"socketCups,bool"`
	SocketGpgAgent   bool `json:"socketGpgAgent" flag:"socketGpgAgent,bool"`
	SocketAtSpiBus   bool `json:"socketAtSpiBus" flag:"socketAtSpiBus,bool"`
	SocketBluetooth  bool `json:"socketBluetooth" flag:"socketBluetooth,bool"`

	DeviceDri   bool `json:"deviceDri" flag:"deviceDri,bool"`
	DeviceKvm   bool `json:"deviceKvm" flag:"deviceKvm,bool"`
	DeviceShm   bool `json:"deviceShm" flag:"deviceShm,bool"`
	DeviceAlsa  bool `json:"deviceAlsa" flag:"deviceAlsa,bool"`
	DeviceVideo bool `json:"deviceVideo" flag:"deviceVideo,bool"`
	DeviceFuse  bool `json:"deviceFuse" flag:"deviceFuse,bool"`
	DeviceTun   bool `json:"deviceTun" flag:"deviceTun,bool"`
	DeviceUsb   bool `json:"deviceUsb" flag:"deviceUsb,bool"`
	DeviceAll   bool `json:"deviceAll" flag:"deviceAll,bool"`

	Notification bool `json:"notification" flag:"notification,bool"`

	FsHost     bool     `json:"fsHost" flag:"fsHost,bool"`
	FsHostEtc  bool     `json:"fsHostEtc" flag:"fsHostEtc,bool"`
	FsHostHome bool     `json:"fsHostHome" flag:"fsHostHome,bool"`
	FsExtra    []string `json:"fsExtra" flag:"fsExtra,strings"`

	Env     []string `json:"env" flag:"env,strings"`
	Network bool     `json:"network" flag:"network,bool"`
	Process bool     `json:"process" flag:"process,bool"`

	AsRoot bool `json:"asRoot" flag:"asRoot,bool"`

	AllowedHostCommands []string `json:"allowedHostCommands" flag:"allowedHostCommands,strings"`
}

func NewOverride() Override {
	return Override{
		SocketX11:           true,
		SocketWayland:       true,
		SocketPulseAudio:    true,
		SocketSessionBus:    true,
		SocketSystemBus:     true,
		SocketSshAgent:      false,
		SocketCups:          true,
		SocketGpgAgent:      false,
		SocketAtSpiBus:      true,
		DeviceDri:           true,
		DeviceKvm:           true,
		DeviceShm:           true,
		DeviceAll:           false,
		FsHost:              false,
		FsHostEtc:           false,
		FsHostHome:          true,
		FsExtra:             []string{},
		Env:                 []string{},
		Network:             true,
		Process:             false,
		AsRoot:              false,
		AllowedHostCommands: []string{},
	}
}
