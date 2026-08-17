/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Mounts returns the list of paths to be mounted on the new namespace
// to achieve the desired override.
func GetOverrideMounts(o types.Override) (mounts, shims []string) {
	curUid := fmt.Sprintf("%d", os.Getuid())

	if o.SocketX11 {
		mounts = append(mounts, "/tmp/.X11-unix/")
		mounts = append(mounts, "/tmp/.ICE-unix/")
		mounts = append(mounts, "/tmp/.XIM-unix/")
		mounts = append(mounts, "/tmp/.font-unix/")
		mounts = append(mounts, "/run/user/"+curUid+"/ICEauthority")
	}

	if o.SocketWayland {
		mounts = append(mounts, waylandSocketMounts(waylandSocketPath(curUid))...)
	}

	if o.SocketX11 && o.SocketWayland {
		xauthority := os.Getenv("XAUTHORITY")
		if xauthority != "" {
			mounts = append(mounts, xauthority)
		} else {
			files, err := filepath.Glob("/run/user/" + curUid + "/.*-Xwaylandauth.*")
			if err == nil {
				mounts = append(mounts, files...)
			}
		}
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

	if o.SocketAtSpiBus {
		mounts = append(mounts, atSpiSocketPaths(curUid)...)
	}
	if o.SocketBluetooth {
		mounts = append(mounts, "/run/dbus/system_bus_socket")
	}

	if o.DeviceAll {
		mounts = append(mounts, "/dev/")
	} else {
		if o.DeviceDri {
			mounts = append(mounts, "/dev/dri/")
			if devices, err := filepath.Glob("/dev/nvidia*"); err == nil {
				mounts = append(mounts, devices...)
			}
		}

		if o.DeviceKvm {
			mounts = append(mounts, "/dev/kvm")
		}

		if o.DeviceShm {
			mounts = append(mounts, "/dev/shm/")
		}

		if o.DeviceAlsa {
			mounts = append(mounts, "/dev/snd/")
		}

		if o.DeviceVideo {
			if vids, err := filepath.Glob("/dev/video*"); err == nil {
				mounts = append(mounts, vids...)
			}
		}

		if o.DeviceFuse {
			mounts = append(mounts, "/dev/fuse")
		}

		if o.DeviceTun {
			mounts = append(mounts, "/dev/net/tun")
		}

		if o.DeviceUsb {
			mounts = append(mounts, "/dev/bus/usb/")
			mounts = append(mounts, "/dev/usb/")
		}

		if o.DeviceInput {
			mounts = append(mounts, "/dev/input/")
		}

		if o.DeviceTTY {
			mounts = append(mounts, "/dev/tty")
		}
	}

	if o.FsHostEtc {
		mounts = append(mounts, "/etc/")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = os.Getenv("HOME")
	}
	if !strings.HasSuffix(homeDir, "/") {
		homeDir += "/"
	}
	if o.FsHostHome {
		mounts = append(mounts, homeDir)
	}

	// TODO: currently always exposed, refer to cmd/spawn.go
	// if o.Process {
	// 	mounts = append(mounts, "/proc/")
	// }

	mounts = append(mounts, o.FsExtra...)

	// foundMounts := []string{}
	// for _, mount := range tools.GetHostMounts() {
	// 	found := false
	// 	for _, m := range mounts {
	// 		if strings.Contains(mount, m) {
	// 			found = true
	// 			break
	// 		}
	// 	}
	// 	if found {
	// 		continue
	// 	}

	// 	if strings.HasPrefix(mount, homeDir) && o.FsHostHome {
	// 		foundMounts = append(foundMounts, mount)
	// 		continue
	// 	}

	// 	for _, m := range o.FsExtra {
	// 		if strings.HasPrefix(mount, m) {
	// 			foundMounts = append(foundMounts, mount)
	// 			break
	// 		}
	// 	}
	// }
	// mounts = append(mounts, foundMounts...)

	return uniqueStrings(mounts), uniqueStrings(shims)
}

func waylandSocketPath(uid string) string {
	runtimeDir := filepath.Join("/run/user", uid)
	display := waylandDisplay(uid)
	if filepath.IsAbs(display) {
		return display
	}
	return filepath.Join(runtimeDir, display)
}

func waylandSocketMounts(socket string) []string {
	mounts := []string{socket}
	if _, err := os.Stat(socket + ".lock"); err == nil {
		mounts = append(mounts, socket+".lock")
	}
	return mounts
}

func waylandDisplay(uid string) string {
	runtimeDir := filepath.Join("/run/user", uid)
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	if filepath.IsAbs(display) {
		clean := filepath.Clean(display)
		if strings.HasPrefix(clean, runtimeDir+string(filepath.Separator)) {
			return clean
		}
		return "wayland-0"
	}
	if filepath.Base(display) != display {
		return "wayland-0"
	}
	return display
}

func systemBrokerShims(o types.Override) []string {
	shims := []string{}
	if o.Notification {
		shims = append(shims, "notify-send")
	}
	if o.OpenURI {
		shims = append(shims, "xdg-open", "gio")
	}
	if o.HostApplications {
		shims = append(shims, "cpak-launch-app")
	}
	if o.FilePicker.Enabled() {
		shims = append(shims, "cpak-file-picker")
	}
	if len(types.HostActionCapabilities(o.HostActions, types.HostActionProviderContainers)) > 0 {
		shims = append(shims, "podman", "docker")
	}
	return shims
}

func atSpiSocketPaths(uid string) []string {
	base := "/run/user/" + uid + "/at-spi"
	paths := atSpiAddressPaths(os.Getenv("AT_SPI_BUS_ADDRESS"), base)
	if discovered, err := filepath.Glob(base + "/bus*"); err == nil {
		paths = append(paths, discovered...)
	}
	available := make([]string, 0, len(paths))
	for _, path := range uniqueStrings(paths) {
		info, err := os.Stat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			available = append(available, path)
		}
	}
	return available
}

func atSpiAddressPaths(address, base string) []string {
	address = strings.TrimPrefix(address, "unix:")
	paths := []string{}
	for _, option := range strings.Split(address, ",") {
		path := strings.TrimPrefix(option, "path=")
		if path != option && filepath.Clean(path) == path && strings.HasPrefix(path, base+"/") {
			paths = append(paths, path)
		}
	}
	return uniqueStrings(paths)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// NewOverride returns a new override with default values.
func NewOverride() types.Override {
	return types.NewOverride()
}

// LoadOverride loads an override from its name.
const overrideSizeLimit = 1 << 20

func validateOverrideFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("override %s is not a regular file", path)
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("override %s is writable by other users", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("override %s does not belong to the caller", path)
	}
	return nil
}

func LoadOverride(origin, version string) (override types.Override, err error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	cpakLocalDir, err := getCpakLocalName(origin)
	if err != nil {
		return
	}

	overridePath := filepath.Join(homeDir, ".config/cpak/overrides", cpakLocalDir, version)
	path := filepath.Join(overridePath, "cpak.json")
	// The override decides the sandbox policy, so it is read under the same
	// conditions the authority applies to the files it trusts: a real file,
	// owned by the caller, not writable by anyone else.
	if err = validateOverrideFile(path); err != nil {
		return types.Override{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	override = types.NewOverride()
	decoder := json.NewDecoder(io.LimitReader(file, overrideSizeLimit))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&override)
	if err != nil {
		return
	}
	if err = types.ValidateFilesystemPermissions(override.Filesystem); err != nil {
		return types.Override{}, err
	}
	if err = migrateLegacyHostCommands(&override); err != nil {
		return types.Override{}, err
	}
	if err = types.ValidateHostActions(override.HostActions); err != nil {
		return types.Override{}, err
	}

	return
}

// Save saves the override in the user's home directory.
func SaveOverride(override types.Override, name, version string) (err error) {
	if err = types.ValidateFilesystemPermissions(override.Filesystem); err != nil {
		return err
	}
	if err = migrateLegacyHostCommands(&override); err != nil {
		return err
	}
	if err = types.ValidateHostActions(override.HostActions); err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	cpakLocalDir, err := getCpakLocalName(name)
	if err != nil {
		return err
	}
	overridePath := filepath.Join(homeDir, ".config/cpak/overrides", cpakLocalDir, version)
	err = os.MkdirAll(overridePath, 0755)
	if err != nil {
		return
	}

	file, err := os.Create(filepath.Join(overridePath, "cpak.json"))
	if err != nil {
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(override)
}

// Delete deletes the override from the user's home directory.
func DeleteOverride(o types.Override, name string) (err error) {
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

// ParseOverride parses the given string and returns an override.
func ParseOverride(override string) (o types.Override) {
	err := json.Unmarshal([]byte(override), &o)
	if err != nil {
		return NewOverride()
	}
	return
}

// StringOverride returns the string representation of the given override.
func StringOverride(o types.Override) (override string) {
	b, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	override = string(b)
	return
}
