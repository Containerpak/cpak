/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

type Override struct {
	SocketX11        bool `json:"socketX11" jsonschema:"description=Mount /tmp/.X11-unix/,default=true" flag:"socketX11,bool"`
	SocketWayland    bool `json:"socketWayland" jsonschema:"description=Mount Wayland socket,default=true" flag:"socketWayland,bool"`
	SocketPulseAudio bool `json:"socketPulseAudio" jsonschema:"description=Mount PulseAudio socket,default=true" flag:"socketPulseAudio,bool"`
	SocketSessionBus bool `json:"socketSessionBus" jsonschema:"description=Mount session DBus socket,default=true" flag:"socketSessionBus,bool"`
	SocketSystemBus  bool `json:"socketSystemBus" jsonschema:"description=Mount system DBus socket,default=true" flag:"socketSystemBus,bool"`
	SocketSshAgent   bool `json:"socketSshAgent" jsonschema:"description=Mount SSH agent socket,default=false" flag:"socketSshAgent,bool"`
	SocketCups       bool `json:"socketCups" jsonschema:"description=Mount CUPS socket,default=true" flag:"socketCups,bool"`
	SocketGpgAgent   bool `json:"socketGpgAgent" jsonschema:"description=Mount GPG agent socket,default=false" flag:"socketGpgAgent,bool"`
	SocketAtSpiBus   bool `json:"socketAtSpiBus" jsonschema:"description=Mount AT-SPI bus socket,default=true" flag:"socketAtSpiBus,bool"`
	SocketBluetooth  bool `json:"socketBluetooth" jsonschema:"description=Mount Bluetooth socket,default=false" flag:"socketBluetooth,bool"`

	DeviceDri   bool `json:"deviceDri" jsonschema:"description=Expose /dev/dri,default=true" flag:"deviceDri,bool"`
	DeviceKvm   bool `json:"deviceKvm" jsonschema:"description=Expose /dev/kvm,default=true" flag:"deviceKvm,bool"`
	DeviceShm   bool `json:"deviceShm" jsonschema:"description=Expose /dev/shm,default=true" flag:"deviceShm,bool"`
	DeviceAlsa  bool `json:"deviceAlsa" jsonschema:"description=Expose ALSA devices,default=false" flag:"deviceAlsa,bool"`
	DeviceVideo bool `json:"deviceVideo" jsonschema:"description=Expose video devices,default=false" flag:"deviceVideo,bool"`
	DeviceFuse  bool `json:"deviceFuse" jsonschema:"description=Expose FUSE devices,default=false" flag:"deviceFuse,bool"`
	DeviceTun   bool `json:"deviceTun" jsonschema:"description=Expose TUN/TAP,default=false" flag:"deviceTun,bool"`
	DeviceUsb   bool `json:"deviceUsb" jsonschema:"description=Expose USB devices,default=false" flag:"deviceUsb,bool"`
	DeviceAll   bool `json:"deviceAll" jsonschema:"description=Expose all /dev,default=false" flag:"deviceAll,bool"`

	Notification bool `json:"notification" jsonschema:"description=Enable desktop notifications,default=false" flag:"notification,bool"`

	FsHost     bool     `json:"fsHost" jsonschema:"description=Mount host root read-only,default=false" flag:"fsHost,bool"`
	FsHostEtc  bool     `json:"fsHostEtc" jsonschema:"description=Mount host /etc,default=false" flag:"fsHostEtc,bool"`
	FsHostHome bool     `json:"fsHostHome" jsonschema:"description=Mount host home directory,default=true" flag:"fsHostHome,bool"`
	FsExtra    []string `json:"fsExtra" jsonschema:"description=Additional paths to mount,items.pattern=^(?:\\./|\\../|/)?(?:[A-Za-z0-9_\\-\\.]+/)*[A-Za-z0-9_\\-\\.]+$,minItems=0" flag:"fsExtra,strings"`

	Env     []string `json:"env" jsonschema:"description=Additional environment variables,items.pattern=^[A-Za-z_][A-Za-z0-9_]*=.+$,minItems=0" flag:"env,strings"`
	Network bool     `json:"network" jsonschema:"description=Enable network namespace,default=true" flag:"network,bool"`
	Process bool     `json:"process" jsonschema:"description=Share host process namespace,default=false" flag:"process,bool"`

	AsRoot bool `json:"asRoot" jsonschema:"description=Run as root inside container,default=false" flag:"asRoot,bool"`

	AllowedHostCommands []string `json:"allowedHostCommands" jsonschema:"description=Host commands allowed via shim,items.pattern=^[A-Za-z0-9_\\-]+$,minItems=0" flag:"allowedHostCommands,strings"`
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
