/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"path"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// OverrideMounts answers the paths a policy puts inside the namespace, and the
// shims that go with it, for the given host.
//
// This is the whole of what a permission does. A permission is not a flag the
// runtime consults later: turning one on adds a path here and turning it off
// removes one, so the list below is the sandbox, written out.
func OverrideMounts(o types.Override, h Host) (mounts, shims []string) {
	curUid := h.uid()

	if o.SocketX11 {
		mounts = append(mounts, "/tmp/.X11-unix/")
		mounts = append(mounts, "/tmp/.ICE-unix/")
		mounts = append(mounts, "/tmp/.XIM-unix/")
		mounts = append(mounts, "/tmp/.font-unix/")
		mounts = append(mounts, "/run/user/"+curUid+"/ICEauthority")
	}

	if o.SocketWayland {
		mounts = append(mounts, WaylandSocketMounts(WaylandSocketPath(h), h)...)
	}

	if o.SocketX11 && o.SocketWayland {
		xauthority := h.env("XAUTHORITY")
		if xauthority != "" {
			mounts = append(mounts, xauthority)
		} else {
			mounts = append(mounts, h.glob("/run/user/"+curUid+"/.*-Xwaylandauth.*")...)
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
		mounts = append(mounts, AtSpiSocketPaths(h)...)
	}
	if o.SocketBluetooth {
		mounts = append(mounts, "/run/dbus/system_bus_socket")
	}

	if o.DeviceAll {
		mounts = append(mounts, "/dev/")
	} else {
		if o.DeviceDri {
			mounts = append(mounts, "/dev/dri/")
			mounts = append(mounts, h.glob("/dev/nvidia*")...)
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
			mounts = append(mounts, h.glob("/dev/video*")...)
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

		// The globs are resolved when the container is built, so a port plugged
		// in later is not there. That is the same bargain deviceVideo and the
		// nvidia nodes already make, and the fix for all three is the devices
		// provider rather than a wider mount here.
		if o.DeviceSerial {
			for _, pattern := range []string{"/dev/ttyUSB*", "/dev/ttyACM*"} {
				mounts = append(mounts, h.glob(pattern)...)
			}
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

	if o.FsHostHome {
		mounts = append(mounts, h.homeMount())
	}

	for _, permission := range o.Filesystem {
		source, _, err := ResolveFilesystem(permission, h)
		if err == nil {
			mounts = append(mounts, source)
		}
	}

	// TODO: currently always exposed, refer to cmd/spawn.go
	// if o.Process {
	// 	mounts = append(mounts, "/proc/")
	// }

	mounts = append(mounts, o.FsExtra...)

	return UniqueStrings(mounts), UniqueStrings(shims)
}

// SystemBrokerShims answers the commands the container gets instead of the host
// programs they are named after. Each one is a permission that cannot be a
// mount: nothing is exposed, a request is forwarded.
func SystemBrokerShims(o types.Override) []string {
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
	if len(types.HostActionCapabilities(o.HostActions, types.HostActionProviderCpak)) > 0 {
		shims = append(shims, "cpak-host")
	}
	return shims
}

// WaylandSocketPath answers the compositor socket this host is using.
func WaylandSocketPath(h Host) string {
	runtimeDir := path.Join("/run/user", h.uid())
	display := WaylandDisplay(h)
	if path.IsAbs(display) {
		return display
	}
	return path.Join(runtimeDir, display)
}

// WaylandSocketMounts answers the socket and, where the compositor keeps one,
// the lock beside it.
func WaylandSocketMounts(socket string, h Host) []string {
	mounts := []string{socket}
	if h.exists(socket + ".lock") {
		mounts = append(mounts, socket+".lock")
	}
	return mounts
}

// WaylandDisplay answers the display name to trust, which is the one the
// session set unless it points somewhere outside the user runtime directory.
// A display cpak cannot vouch for falls back to wayland-0 rather than mounting
// whatever the variable named.
func WaylandDisplay(h Host) string {
	runtimeDir := path.Join("/run/user", h.uid())
	display := h.env("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	if path.IsAbs(display) {
		clean := path.Clean(display)
		if strings.HasPrefix(clean, runtimeDir+"/") {
			return clean
		}
		return "wayland-0"
	}
	if path.Base(display) != display {
		return "wayland-0"
	}
	return display
}

// AtSpiSocketPaths answers the accessibility bus sockets that are actually
// there. An address is read from the session only where it names a path inside
// the user runtime directory, and a path that is not a socket is dropped.
func AtSpiSocketPaths(h Host) []string {
	base := "/run/user/" + h.uid() + "/at-spi"
	paths := AtSpiAddressPaths(h.env("AT_SPI_BUS_ADDRESS"), base)
	paths = append(paths, h.glob(base+"/bus*")...)
	available := make([]string, 0, len(paths))
	for _, candidate := range UniqueStrings(paths) {
		if h.isSocket(candidate) {
			available = append(available, candidate)
		}
	}
	return available
}

// AtSpiAddressPaths reads the paths out of a D-Bus address, keeping only the
// clean ones under the given base.
func AtSpiAddressPaths(address, base string) []string {
	address = strings.TrimPrefix(address, "unix:")
	paths := []string{}
	for _, option := range strings.Split(address, ",") {
		candidate := strings.TrimPrefix(option, "path=")
		if candidate != option && path.Clean(candidate) == candidate && strings.HasPrefix(candidate, base+"/") {
			paths = append(paths, candidate)
		}
	}
	return UniqueStrings(paths)
}

// UniqueStrings keeps the first occurrence of every non-empty value, because a
// mount list is read in order and a repeated path is a second bind of the same
// thing rather than a second thing.
func UniqueStrings(values []string) []string {
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
