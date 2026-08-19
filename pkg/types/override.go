/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

type Override struct {
	SocketX11        bool `json:"socketX11" jsonschema:"description=Mount /tmp/.X11-unix/,default=false" flag:"socketX11,bool"`
	SocketWayland    bool `json:"socketWayland" jsonschema:"description=Mount Wayland socket,default=false" flag:"socketWayland,bool"`
	SocketPulseAudio bool `json:"socketPulseAudio" jsonschema:"description=Mount PulseAudio socket,default=false" flag:"socketPulseAudio,bool"`
	SocketSessionBus bool `json:"socketSessionBus" jsonschema:"description=Mount session DBus socket,default=false" flag:"socketSessionBus,bool"`
	SocketSystemBus  bool `json:"socketSystemBus" jsonschema:"description=Mount system DBus socket,default=false" flag:"socketSystemBus,bool"`
	SocketSshAgent   bool `json:"socketSshAgent" jsonschema:"description=Mount SSH agent socket,default=false" flag:"socketSshAgent,bool"`
	SocketCups       bool `json:"socketCups" jsonschema:"description=Mount CUPS socket,default=false" flag:"socketCups,bool"`
	SocketGpgAgent   bool `json:"socketGpgAgent" jsonschema:"description=Mount GPG agent socket,default=false" flag:"socketGpgAgent,bool"`
	SocketAtSpiBus   bool `json:"socketAtSpiBus" jsonschema:"description=Mount AT-SPI bus socket,default=false" flag:"socketAtSpiBus,bool"`
	SocketBluetooth  bool `json:"socketBluetooth" jsonschema:"description=Mount Bluetooth socket,default=false" flag:"socketBluetooth,bool"`

	DeviceDri   bool `json:"deviceDri" jsonschema:"description=Expose /dev/dri,default=false" flag:"deviceDri,bool"`
	DeviceKvm   bool `json:"deviceKvm" jsonschema:"description=Expose /dev/kvm,default=false" flag:"deviceKvm,bool"`
	DeviceShm   bool `json:"deviceShm" jsonschema:"description=Expose /dev/shm,default=false" flag:"deviceShm,bool"`
	DeviceAlsa  bool `json:"deviceAlsa" jsonschema:"description=Expose ALSA devices,default=false" flag:"deviceAlsa,bool"`
	DeviceVideo bool `json:"deviceVideo" jsonschema:"description=Expose video devices,default=false" flag:"deviceVideo,bool"`
	DeviceFuse  bool `json:"deviceFuse" jsonschema:"description=Expose FUSE devices,default=false" flag:"deviceFuse,bool"`
	DeviceTun   bool `json:"deviceTun" jsonschema:"description=Expose TUN/TAP,default=false" flag:"deviceTun,bool"`
	DeviceUsb   bool `json:"deviceUsb" jsonschema:"description=Expose USB devices,default=false" flag:"deviceUsb,bool"`
	// A serial port used to cost deviceAll, which is the whole of /dev, because
	// nothing here named one. Boards, printers, radios and meters all arrive as
	// ttyUSB or ttyACM, so they get their own permission rather than the
	// escalation that was the only way to reach them.
	DeviceSerial bool `json:"deviceSerial" jsonschema:"description=Expose USB and CDC serial ports,default=false" flag:"deviceSerial,bool"`
	DeviceInput  bool `json:"deviceInput" jsonschema:"description=Expose input devices,default=false" flag:"deviceInput,bool"`
	DeviceTTY    bool `json:"deviceTTY" jsonschema:"description=Expose the controlling terminal,default=false" flag:"deviceTTY,bool"`
	DeviceAll    bool `json:"deviceAll" jsonschema:"description=Expose all /dev,default=false" flag:"deviceAll,bool"`

	Notification     bool              `json:"notification" jsonschema:"description=Enable desktop notifications,default=false" flag:"notification,bool"`
	OpenURI          bool              `json:"openURI" jsonschema:"description=Allow opening external URIs,default=false" flag:"openURI,bool"`
	HostApplications bool              `json:"hostApplications" jsonschema:"description=Expose and launch host desktop applications,default=false" flag:"hostApplications,bool"`
	HostActions      []HostActionGrant `json:"hostActions,omitempty" jsonschema:"description=Typed host service capabilities"`
	FilePicker       FilePickerGrant   `json:"filePicker,omitempty" jsonschema:"description=Native file chooser capabilities"`

	Filesystem []FilesystemPermission `json:"filesystem,omitempty" jsonschema:"description=Host filesystem permissions"`

	FsHost     bool     `json:"fsHost,omitempty" jsonschema:"description=Legacy v1 host root permission" flag:"fsHost,bool"`
	FsHostEtc  bool     `json:"fsHostEtc,omitempty" jsonschema:"description=Legacy v1 host etc permission" flag:"fsHostEtc,bool"`
	FsHostHome bool     `json:"fsHostHome,omitempty" jsonschema:"description=Legacy v1 host home permission" flag:"fsHostHome,bool"`
	FsExtra    []string `json:"fsExtra,omitempty" jsonschema:"description=Legacy v1 additional paths" flag:"fsExtra,strings"`

	Env            []string `json:"env,omitempty" jsonschema:"description=Additional environment variables,minItems=0" flag:"env,strings"`
	Network        bool     `json:"network" jsonschema:"description=Enable network namespace,default=false" flag:"network,bool"`
	Process        bool     `json:"process" jsonschema:"description=Share host process namespace,default=false" flag:"process,bool"`
	UserNamespaces bool     `json:"userNamespaces" jsonschema:"description=Allow nested user namespaces for application sandboxes,default=false" flag:"userNamespaces,bool"`

	MemoryMaxMB int `json:"memoryMaxMB" jsonschema:"minimum=0,description=Maximum memory in MiB,default=0" flag:"memoryMaxMB,int"`
	CPUQuota    int `json:"cpuQuota" jsonschema:"minimum=0,maximum=1000,description=CPU quota as a percentage of one core,default=0" flag:"cpuQuota,int"`
	PidsMax     int `json:"pidsMax" jsonschema:"minimum=0,description=Maximum process count,default=0" flag:"pidsMax,int"`

	AsRoot bool `json:"asRoot" jsonschema:"description=Run as root inside container,default=false" flag:"asRoot,bool"`

	AllowedHostCommands []string `json:"allowedHostCommands,omitempty" jsonschema:"description=Legacy host command compatibility field,minItems=0"`
}

type FilePickerGrant struct {
	OpenFile         bool `json:"openFile,omitempty" jsonschema:"description=Select an existing host file,default=false"`
	OpenFolder       bool `json:"openFolder,omitempty" jsonschema:"description=Select an existing host folder,default=false"`
	SaveFile         bool `json:"saveFile,omitempty" jsonschema:"description=Select a host destination for a new file,default=false"`
	Persistent       bool `json:"persistent,omitempty" jsonschema:"description=Offer persistent grants,default=false"`
	ContainingFolder bool `json:"containingFolder,omitempty" jsonschema:"description=Offer the containing folder as context,default=false"`
}

func (g FilePickerGrant) Enabled() bool {
	return g.OpenFile || g.OpenFolder || g.SaveFile
}

func ValidateFilePickerGrant(grant FilePickerGrant) error {
	if !grant.Enabled() && (grant.Persistent || grant.ContainingFolder) {
		return errors.New("file picker options require an enabled picker mode")
	}
	if grant.ContainingFolder && !grant.OpenFile {
		return errors.New("file picker containing folder access requires openFile")
	}
	return nil
}

// NewOverride is what a manifest gets for every permission it does not mention,
// and the answer is now none of them.
//
// It used to hand out the session bus, the system bus, X11, Wayland, PulseAudio,
// CUPS, AT-SPI, the GPU, KVM, shared memory and the network to anything that
// stayed quiet. The session bus alone is the whole sandbox: with it the proxy in
// pkg/desktopbus stops filtering, and a name away sits org.freedesktop.systemd1
// and a StartTransientUnit that runs a process outside the container. A
// permission nobody asked for is not a default, it is a grant, and a package
// that needs one can say so in one line.
//
// This is a breaking change for a manifest that relied on a default, and that
// is the intended direction: the failure is an application that cannot reach
// something until its manifest asks, rather than one that reaches everything
// because its manifest was silent.
func NewOverride() Override {
	return Override{
		SocketX11:           false,
		SocketWayland:       false,
		SocketPulseAudio:    false,
		SocketSessionBus:    false,
		SocketSystemBus:     false,
		SocketSshAgent:      false,
		SocketCups:          false,
		SocketGpgAgent:      false,
		SocketAtSpiBus:      false,
		DeviceDri:           false,
		DeviceKvm:           false,
		DeviceShm:           false,
		DeviceAll:           false,
		HostApplications:    false,
		Filesystem:          []FilesystemPermission{},
		FsHost:              false,
		FsHostEtc:           false,
		FsHostHome:          false,
		FsExtra:             []string{},
		Env:                 []string{},
		Network:             false,
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
	Path   string `json:"path" jsonschema:"pattern=^(?:home(?:/.*)?|host|xdg-(?:desktop|documents|download|music|pictures|public-share|templates|videos)|/.*)$,description=Host path or portable home host and XDG scope"`
	Access string `json:"access" jsonschema:"enum=read-only,enum=read-write,description=Filesystem access mode"`
}

func (p FilesystemPermission) String() string {
	return p.Path + " (" + p.Access + ")"
}

// WithMigratedFilesystem reads a policy's filesystem grants in one form: the
// typed grants, with the legacy v1 fields written out as the grants they stand
// for. It is the whole of what fsHost, fsHostEtc, fsHostHome and fsExtra ever
// meant, in one place, so that everything asking whether two policies are the
// same restriction asks it of the same table.
//
// A policy that carries none of the legacy fields is returned untouched, which
// is every policy a v2 manifest can produce.
//
// This is a reading, not a rewrite. Nothing that hashes a policy calls it: the
// hash names the policy an anchor was recorded over, byte for byte, and a
// reading that moved it would leave every anchor already in the ledger naming
// something nobody can derive again.
func (o Override) WithMigratedFilesystem() Override {
	if !o.FsHost && !o.FsHostEtc && !o.FsHostHome && len(o.FsExtra) == 0 {
		return o
	}
	filesystem := append([]FilesystemPermission{}, o.Filesystem...)
	if o.FsHost {
		filesystem = append(filesystem, FilesystemPermission{Path: "host", Access: "read-only"})
	}
	if o.FsHostEtc {
		filesystem = append(filesystem, FilesystemPermission{Path: "/etc", Access: "read-only"})
	}
	if o.FsHostHome {
		filesystem = append(filesystem, FilesystemPermission{Path: "home", Access: "read-write"})
	}
	for _, path := range o.FsExtra {
		filesystem = append(filesystem, FilesystemPermission{Path: path, Access: "read-write"})
	}
	migrated := o
	migrated.Filesystem = filesystem
	migrated.FsHost = false
	migrated.FsHostEtc = false
	migrated.FsHostHome = false
	migrated.FsExtra = nil
	return migrated
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
		case reflect.Struct:
			if key == "filePicker" && filePickerHasAdditions(o.FilePicker, next.FilePicker) {
				changes = append(changes, key)
			}
		}
	}
	return changes
}

func filePickerHasAdditions(current, next FilePickerGrant) bool {
	return !current.OpenFile && next.OpenFile ||
		!current.OpenFolder && next.OpenFolder ||
		!current.SaveFile && next.SaveFile ||
		!current.Persistent && next.Persistent ||
		!current.ContainingFolder && next.ContainingFolder
}

// UngrantedPermissions names the permissions a manifest did not mention.
//
// It exists because the decoded struct cannot answer the question: a
// permission written as false and one nobody wrote both arrive as false, and
// the difference is what an author needs told. Nothing is granted by omission,
// so a manifest that stays quiet about the display or the session bus is a
// manifest whose application will not have them, and saying so while it is
// being written is cheaper than saying it after it ships.
//
// Only the permissions a manifest is expected to state are considered. The
// fields that are omitted when empty, such as the filesystem list or the host
// actions, are absent by design and are not reported.
func UngrantedPermissions(raw []byte) ([]string, error) {
	var manifest struct {
		Override map[string]json.RawMessage `json:"override"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	fields := reflect.TypeOf(Override{})
	missing := []string{}
	for index := 0; index < fields.NumField(); index++ {
		field := fields.Field(index)
		if field.Type.Kind() != reflect.Bool {
			continue
		}
		tag := field.Tag.Get("json")
		key, options, _ := strings.Cut(tag, ",")
		if key == "" || strings.Contains(options, "omitempty") {
			continue
		}
		if _, written := manifest.Override[key]; !written {
			missing = append(missing, key)
		}
	}
	return missing, nil
}
