/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package types

import (
	"reflect"
	"strings"
)

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
	DeviceInput bool `json:"deviceInput" jsonschema:"description=Expose input devices,default=false" flag:"deviceInput,bool"`
	DeviceTTY   bool `json:"deviceTTY" jsonschema:"description=Expose the controlling terminal,default=false" flag:"deviceTTY,bool"`
	DeviceAll   bool `json:"deviceAll" jsonschema:"description=Expose all /dev,default=false" flag:"deviceAll,bool"`

	Notification     bool              `json:"notification" jsonschema:"description=Enable desktop notifications,default=false" flag:"notification,bool"`
	OpenURI          bool              `json:"openURI" jsonschema:"description=Allow opening external URIs,default=false" flag:"openURI,bool"`
	HostApplications bool              `json:"hostApplications" jsonschema:"description=Expose and launch host desktop applications,default=false" flag:"hostApplications,bool"`
	HostActions      []HostActionGrant `json:"hostActions,omitempty" jsonschema:"description=Typed host service capabilities"`

	Filesystem []FilesystemPermission `json:"filesystem,omitempty" jsonschema:"description=Host filesystem permissions"`

	FsHost     bool     `json:"fsHost,omitempty" jsonschema:"description=Legacy v1 host root permission" flag:"fsHost,bool"`
	FsHostEtc  bool     `json:"fsHostEtc,omitempty" jsonschema:"description=Legacy v1 host etc permission" flag:"fsHostEtc,bool"`
	FsHostHome bool     `json:"fsHostHome,omitempty" jsonschema:"description=Legacy v1 host home permission" flag:"fsHostHome,bool"`
	FsExtra    []string `json:"fsExtra,omitempty" jsonschema:"description=Legacy v1 additional paths" flag:"fsExtra,strings"`

	Env            []string `json:"env,omitempty" jsonschema:"description=Additional environment variables,items.pattern=^[A-Za-z_][A-Za-z0-9_]*=.+$,minItems=0" flag:"env,strings"`
	Network        bool     `json:"network" jsonschema:"description=Enable network namespace,default=true" flag:"network,bool"`
	Process        bool     `json:"process" jsonschema:"description=Share host process namespace,default=false" flag:"process,bool"`
	UserNamespaces bool     `json:"userNamespaces" jsonschema:"description=Allow nested user namespaces for application sandboxes,default=false" flag:"userNamespaces,bool"`

	MemoryMaxMB int `json:"memoryMaxMB" jsonschema:"minimum=0,description=Maximum memory in MiB,default=0" flag:"memoryMaxMB,int"`
	CPUQuota    int `json:"cpuQuota" jsonschema:"minimum=0,maximum=1000,description=CPU quota as a percentage of one core,default=0" flag:"cpuQuota,int"`
	PidsMax     int `json:"pidsMax" jsonschema:"minimum=0,description=Maximum process count,default=0" flag:"pidsMax,int"`

	AsRoot bool `json:"asRoot" jsonschema:"description=Run as root inside container,default=false" flag:"asRoot,bool"`

	AllowedHostCommands []string `json:"allowedHostCommands,omitempty" jsonschema:"description=Legacy host command compatibility field,items.pattern=^[A-Za-z0-9_\\-]+$,minItems=0"`
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
		HostApplications:    false,
		Filesystem:          []FilesystemPermission{},
		FsHost:              false,
		FsHostEtc:           false,
		FsHostHome:          false,
		FsExtra:             []string{},
		Env:                 []string{},
		Network:             true,
		Process:             false,
		UserNamespaces:      false,
		MemoryMaxMB:         0,
		CPUQuota:            0,
		PidsMax:             0,
		AsRoot:              false,
		AllowedHostCommands: nil,
	}
}

type FilesystemPermission struct {
	Path   string `json:"path" jsonschema:"pattern=^(?:home|host|xdg-(?:desktop|documents|download|music|pictures|public-share|templates|videos)|/.*)$,description=Host path or portable home host and XDG scope"`
	Access string `json:"access" jsonschema:"enum=read-only,enum=read-write,description=Filesystem access mode"`
}

func (p FilesystemPermission) String() string {
	return p.Path + " (" + p.Access + ")"
}

// Diff returns the manifest permission keys whose effective values changed.
func (o Override) Diff(next Override) []string {
	current := reflect.ValueOf(o)
	updated := reflect.ValueOf(next)
	typeOfOverride := current.Type()
	changes := []string{}
	for index := 0; index < current.NumField(); index++ {
		if reflect.DeepEqual(current.Field(index).Interface(), updated.Field(index).Interface()) {
			continue
		}
		key := strings.Split(typeOfOverride.Field(index).Tag.Get("json"), ",")[0]
		if key != "" {
			changes = append(changes, key)
		}
	}
	return changes
}

// Additions returns permissions newly granted by next.
func (o Override) Additions(next Override) []string {
	current := reflect.ValueOf(o)
	updated := reflect.ValueOf(next)
	typeOfOverride := current.Type()
	changes := []string{}
	for index := 0; index < current.NumField(); index++ {
		field := typeOfOverride.Field(index)
		key := strings.Split(field.Tag.Get("json"), ",")[0]
		if key == "" {
			continue
		}
		switch current.Field(index).Kind() {
		case reflect.Bool:
			if !current.Field(index).Bool() && updated.Field(index).Bool() {
				changes = append(changes, key)
			}
		case reflect.Slice:
			for item := 0; item < updated.Field(index).Len(); item++ {
				found := false
				for currentItem := 0; currentItem < current.Field(index).Len(); currentItem++ {
					if reflect.DeepEqual(current.Field(index).Index(currentItem).Interface(), updated.Field(index).Index(item).Interface()) {
						found = true
						break
					}
				}
				if !found {
					changes = append(changes, key)
					break
				}
			}
		}
	}
	return changes
}
