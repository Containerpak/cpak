package types

type Override struct {
	SocketX11        bool `json:"socketX11"`
	SocketWayland    bool `json:"socketWayland"`
	SocketPulseAudio bool `json:"socketPulseAudio"`
	SocketSessionBus bool `json:"socketSessionBus"`
	SocketSystemBus  bool `json:"socketSystemBus"`
	SocketSshAgent   bool `json:"socketSshAgent"`
	SocketCups       bool `json:"socketCups"`
	SocketGpgAgent   bool `json:"socketGpgAgent"`

	DeviceDri bool `json:"deviceDri"`
	DeviceKvm bool `json:"deviceKvm"`
	DeviceShm bool `json:"deviceShm"`
	DeviceAll bool `json:"deviceAll"`

	FsHost     bool     `json:"fsHost"`
	FsHostEtc  bool     `json:"fsHostEtc"`
	FsHostHome bool     `json:"fsHostHome"`
	FsExtra    []string `json:"fsExtra"`

	Env     []string `json:"env"`
	Network bool     `json:"network"`
	Process bool     `json:"process"`

	AsRoot bool `json:"asRoot"`
}
