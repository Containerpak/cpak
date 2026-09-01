/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package bootstrap

import (
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// SummarizePermissions is the permission list a signed installer shows and
// the install command verifies against the fetched manifest.
func SummarizePermissions(override types.Override) []Permission {
	permissions := []Permission{}
	add := func(enabled bool, name, detail string) {
		if enabled {
			permissions = append(permissions, Permission{Name: name, Detail: detail})
		}
	}

	displays := []string{}
	if override.DisplayX11 {
		displays = append(displays, "isolated X11 compatibility display")
	}
	if override.SocketX11 {
		displays = append(displays, "X11 (no input or screen isolation)")
	}
	if override.SocketWayland {
		displays = append(displays, "Wayland display and compositor-mediated clipboard")
	}
	if len(displays) > 0 {
		permissions = append(permissions, Permission{Name: "Display", Detail: strings.Join(displays, ", ")})
	}
	add(override.SocketPulseAudio, "Audio", "PulseAudio")
	add(override.SocketSessionBus, "Session services", "session D-Bus")
	add(override.SocketSystemBus, "System services", "system D-Bus")
	add(override.SocketSshAgent, "SSH agent", "host authentication socket")
	add(override.SocketCups, "Printing", "CUPS")
	add(override.SocketGpgAgent, "GPG agent", "host signing socket")
	add(override.SocketAtSpiBus, "Accessibility", "AT-SPI")
	add(override.SocketBluetooth, "Bluetooth", "Bluetooth socket")
	add(override.Bluetooth, "Bluetooth", "general BlueZ service through a private proxy")

	devices := []string{}
	if override.DeviceAll {
		devices = append(devices, "all devices")
	} else {
		deviceFlags := []struct {
			enabled bool
			name    string
		}{
			{override.DeviceDri, "graphics"},
			{override.DeviceKvm, "KVM"},
			{override.DeviceShm, "shared memory"},
			{override.DeviceAlsa, "ALSA"},
			{override.DeviceVideo, "video"},
			{override.DeviceFuse, "FUSE"},
			{override.DeviceTun, "TUN/TAP"},
			{override.DeviceUsb, "USB"},
			{override.DeviceSerial, "serial ports"},
			{override.DeviceInput, "input devices"},
			{override.DeviceTTY, "controlling terminal"},
		}
		for _, device := range deviceFlags {
			if device.enabled {
				devices = append(devices, device.name)
			}
		}
	}
	if len(devices) > 0 {
		permissions = append(permissions, Permission{Name: "Devices", Detail: strings.Join(devices, ", ")})
	}

	add(override.Notification, "Notifications", "desktop notifications")
	add(override.OpenURI, "External links", "open URIs on the host")
	add(override.HostApplications, "Host applications", "desktop catalog and launch broker")
	picker := []string{}
	if override.FilePicker.OpenFile {
		picker = append(picker, "open files")
	}
	if override.FilePicker.OpenFolder {
		picker = append(picker, "open folders")
	}
	if override.FilePicker.SaveFile {
		picker = append(picker, "save files")
	}
	if override.FilePicker.Persistent {
		picker = append(picker, "persistent grants")
	}
	if override.FilePicker.ContainingFolder {
		picker = append(picker, "containing folders")
	}
	if len(picker) > 0 {
		permissions = append(permissions, Permission{Name: "File chooser", Detail: strings.Join(picker, ", ")})
	}
	for _, rule := range override.SessionBus.Talk {
		permissions = append(permissions, Permission{
			Name:   "Session service",
			Detail: truncatePermission(rule.Name+": "+rule.Interface+"."+strings.Join(rule.Members, ", "), 160),
		})
	}
	for _, name := range override.SessionBus.Own {
		permissions = append(permissions, Permission{Name: "Session bus name", Detail: name})
	}
	for _, filesystem := range override.Filesystem {
		detail := filesystem.Path + ", " + strings.ReplaceAll(filesystem.Access, "-", " ")
		if filesystem.Access == "read-write" && (filesystem.Path == "home" || filesystem.Path == "host" || filesystem.Path == "/") {
			detail += "; can run code on the host through startup files"
		}
		permissions = append(permissions, Permission{
			Name:   "Files",
			Detail: detail,
		})
	}
	add(override.FsHost, "Files", "host, read only")
	add(override.FsHostEtc, "Files", "/etc, read only")
	add(override.FsHostHome, "Files", "home, read and write; can run code on the host through startup files")
	for _, filesystem := range override.FsExtra {
		permissions = append(permissions, Permission{Name: "Files", Detail: filesystem + ", read and write"})
	}
	add(override.Network, "Network", "internet and local network")
	add(override.HostNetwork, "Host network", "shared network namespace, including localhost services and host ports")
	add(override.Process, "Host processes", "shared process namespace")
	add(override.UserNamespaces, "Nested sandboxes", "user namespaces and mount setup; disables Landlock")
	add(override.AsRoot, "Root", "runs as root inside the cpak")
	for _, action := range override.HostActions {
		permissions = append(permissions, Permission{
			Name:   "Host service",
			Detail: truncatePermission(action.Provider+": "+strings.Join(action.Capabilities, ", "), 160),
		})
	}
	return permissions
}

func truncatePermission(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
