package cpak

import (
	"encoding/json"
	"fmt"
	"os"
)

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

// Mounts returns the list of paths to be mounted on the new namespace
// to achieve the desired override.
func (o *Override) Mounts() []string {
	var mounts []string

	curUid := fmt.Sprintf("%d", os.Getuid())

	if o.SocketX11 {
		mounts = append(mounts, "/tmp/.X11-unix")
	}

	if o.SocketWayland {
		mounts = append(mounts, "/run/user/"+curUid+"/wayland-0")
	}

	if o.SocketPulseAudio {
		mounts = append(mounts, "/run/user/"+curUid+"/pulse/native")
	}

	if o.SocketSessionBus {
		mounts = append(mounts, "/run/user/"+curUid+"/bus")
	}

	if o.SocketSystemBus {
		mounts = append(mounts, "/run/dbus/system_bus_socket")
	}

	if o.SocketSshAgent {
		mounts = append(mounts, "/run/user/"+curUid+"/ssh-agent.socket")
	}

	if o.SocketCups {
		mounts = append(mounts, "/run/cups/cups.sock")
	}

	if o.SocketGpgAgent {
		mounts = append(mounts, "/run/user/"+curUid+"/gnupg/S.gpg-agent")
	}

	if o.DeviceAll {
		mounts = append(mounts, "/dev")
	} else {
		if o.DeviceDri {
			mounts = append(mounts, "/dev/dri")
		}

		if o.DeviceKvm {
			mounts = append(mounts, "/dev/kvm")
		}

		if o.DeviceShm {
			mounts = append(mounts, "/dev/shm")
		}
	}

	if o.FsHost {
		mounts = append(mounts, "/")
	}

	if o.FsHostEtc {
		mounts = append(mounts, "/etc")
	}

	if o.FsHostHome {
		mounts = append(mounts, "/home")
	}

	if o.Process {
		mounts = append(mounts, "/proc")
	}

	mounts = append(mounts, o.FsExtra...)

	return mounts
}

// NewOverride returns a new override with default values.
func NewOverride() *Override {
	return &Override{
		SocketX11:        true,
		SocketWayland:    true,
		SocketPulseAudio: true,
		SocketSessionBus: true,
		SocketSystemBus:  true,
		SocketSshAgent:   false,
		SocketCups:       true,
		SocketGpgAgent:   false,

		DeviceDri: true,
		DeviceKvm: true,
		DeviceShm: true,
		DeviceAll: false,

		FsHost:     false,
		FsHostEtc:  false,
		FsHostHome: true,
		FsExtra:    []string{},

		Env:     []string{},
		Network: true,
		Process: false,
	}
}

// LoadOverride loads an override from its name.
func LoadOverride(name string) (override Override, err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	overridesPath := homeDir + "/.config/cpak/overrides"
	err = os.MkdirAll(overridesPath, 0755)
	if err != nil {
		return
	}

	file, err := os.Create(overridesPath + "/" + name + ".json")
	if err != nil {
		return
	}

	err = json.NewDecoder(file).Decode(&override)
	if err != nil {
		return
	}

	return
}

// Save saves the override in the user's home directory.
func (o *Override) Save(name string) (err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	overridesPath := homeDir + "/.config/cpak/overrides"
	err = os.MkdirAll(overridesPath, 0755)
	if err != nil {
		return
	}

	file, err := os.Create(overridesPath + "/" + name + ".json")
	if err != nil {
		return
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(o)
}

// Delete deletes the override from the user's home directory.
func (o *Override) Delete(name string) (err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	overridesPath := homeDir + "/.config/cpak/overrides"
	err = os.MkdirAll(overridesPath, 0755)
	if err != nil {
		return
	}

	err = os.Remove(overridesPath + "/" + name + ".json")
	if err != nil {
		return
	}

	return
}
