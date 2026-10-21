/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"path"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Decision is what cpak made of one permission a Flatpak application asked
// for.
type Decision string

const (
	// Granted is a request cpak has an equivalent for, given as asked.
	Granted Decision = "granted"
	// Narrowed is a request given in a smaller form than Flatpak gives it.
	Narrowed Decision = "narrowed"
	// Refused is a request cpak does not grant to a Flatpak application. The
	// user can still grant the cpak equivalent, where there is one, through
	// an override.
	Refused Decision = "refused"
	// Provided is a request cpak answers itself, without a permission.
	Provided Decision = "provided"
)

// Finding records the decision taken over one request.
type Finding struct {
	Request  string   `json:"request"`
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// Translation is the cpak policy a Flatpak application runs with, and the
// record of how it was reached.
type Translation struct {
	Override types.Override `json:"override"`
	Findings []Finding      `json:"findings"`
}

// Refusals answers with the requests that were not granted in full.
func (t Translation) Refusals() []Finding {
	var values []Finding
	for _, finding := range t.Findings {
		if finding.Decision == Refused || finding.Decision == Narrowed {
			values = append(values, finding)
		}
	}
	return values
}

// sessionNames are the bus names a Flatpak application may ask to talk to
// that cpak serves through its own host actions.
var sessionNames = map[string]string{
	"org.freedesktop.Notifications": "notification",
}

// handledNames are the bus names cpak answers inside the sandbox on its own,
// so a request for them needs no grant and opens nothing on the host bus.
var handledNames = []string{
	"org.freedesktop.portal.",
	"org.freedesktop.Flatpak",
	"org.freedesktop.impl.portal.",
}

// Translate builds the policy for a Flatpak application from its metadata.
//
// The translation only ever narrows. Each request maps onto the cpak
// permission that does the same job, and a request that has no cpak
// equivalent, or whose equivalent is wider than what was asked, is refused
// rather than rounded up: raw buses, the host root, other services' runtime
// sockets, ptrace and every escape hatch Flatpak keeps for itself. What cpak
// itself mediates (the file chooser, opening links) is given as cpak gives it
// to any application, since every use of it goes through the user.
func Translate(meta Metadata) Translation {
	override := types.NewOverride()
	t := &translator{override: &override}

	shared := set(meta.Context.Shared)
	sockets := set(meta.Context.Sockets)
	devices := set(meta.Context.Devices)
	features := set(meta.Context.Features)

	for _, value := range sorted(shared) {
		switch value {
		case "network":
			override.Network = true
			t.add("shared=network", Granted, "")
		case "ipc":
			t.add("shared=ipc", Narrowed, "the application keeps a private IPC namespace")
		default:
			t.add("shared="+value, Refused, "unknown shared resource")
		}
	}

	for _, value := range sorted(sockets) {
		request := "sockets=" + value
		switch value {
		case "wayland":
			override.SocketWayland = true
			t.add(request, Granted, "")
		case "x11", "fallback-x11":
			if sockets["wayland"] {
				t.add(request, Narrowed, "the application uses Wayland, which it also asked for")
				continue
			}
			t.x11(request)
		case "pulseaudio":
			override.SocketPulseAudio = true
			t.add(request, Granted, "")
		case "ssh-auth":
			override.SocketSshAgent = true
			t.add(request, Granted, "")
		case "cups":
			override.SocketCups = true
			t.add(request, Granted, "")
		case "gpg-agent":
			override.SocketGpgAgent = true
			t.add(request, Granted, "")
		case "session-bus":
			t.add(request, Refused, "cpak never shares the raw session bus; the filtered bus policy below still applies")
		case "system-bus":
			t.add(request, Refused, "cpak never shares the raw system bus")
		case "inherit-wayland-socket":
			t.add(request, Refused, "cpak chooses the Wayland socket itself")
		default:
			t.add(request, Refused, "no cpak equivalent")
		}
	}

	for _, value := range sorted(devices) {
		request := "devices=" + value
		switch value {
		case "dri":
			override.DeviceDri = true
			t.add(request, Granted, "")
		case "kvm":
			override.DeviceKvm = true
			t.add(request, Granted, "")
		case "shm":
			override.DeviceShm = true
			t.add(request, Granted, "")
		case "usb":
			override.DeviceUsb = true
			t.add(request, Granted, "")
		case "input":
			override.DeviceInput = true
			t.add(request, Granted, "")
		case "all":
			override.DeviceDri = true
			override.DeviceVideo = true
			t.add(request, Narrowed, "the GPU and cameras are shared; the rest of /dev needs an explicit deviceAll override")
		default:
			t.add(request, Refused, "no cpak equivalent")
		}
	}

	for _, value := range sorted(features) {
		request := "features=" + value
		switch value {
		case "bluetooth":
			override.Bluetooth = true
			t.add(request, Granted, "")
		case "devel":
			t.add(request, Refused, "ptrace and perf stay blocked by the cpak seccomp filter")
		case "multiarch":
			t.add(request, Refused, "32-bit compatibility is not provided")
		default:
			t.add(request, Refused, "no cpak equivalent")
		}
	}

	for _, value := range meta.Context.Filesystems {
		t.filesystem(value)
	}
	for _, value := range meta.Context.Persistent {
		t.add("persistent="+value, Provided, "every cpak application already keeps a private persistent home")
	}

	t.sessionBus(meta.SessionBus)
	for _, name := range sortedKeys(meta.SystemBus) {
		request := "system-bus " + meta.SystemBus[name] + " " + name
		if name == "org.bluez" && override.Bluetooth {
			t.add(request, Provided, "BlueZ is reached through the cpak Bluetooth proxy")
			continue
		}
		t.add(request, Refused, "cpak does not expose system bus services")
	}

	for _, name := range sortedKeys(meta.Environment) {
		if loaderVariable(name) {
			t.add("environment "+name, Refused, "loader variables are never inherited from a package")
			continue
		}
		override.Env = append(override.Env, name+"="+meta.Environment[name])
	}

	// The file chooser and link opening are answered by cpak and every use
	// goes through the user, which is why a Flatpak application gets them
	// without asking, as it gets the portals behind them.
	override.FilePicker = types.FilePickerGrant{OpenFile: true, OpenFolder: true, SaveFile: true}
	override.OpenURI = true
	t.add("portal FileChooser", Provided, "answered by the cpak file chooser, one choice at a time")
	t.add("portal OpenURI", Provided, "answered by cpak for http, https and mailto links")

	override.Filesystem = foldFilesystem(override.Filesystem)
	return Translation{Override: override, Findings: t.findings}
}

type translator struct {
	override *types.Override
	findings []Finding
}

func (t *translator) add(request string, decision Decision, reason string) {
	t.findings = append(t.findings, Finding{Request: request, Decision: decision, Reason: reason})
}

func (t *translator) x11(request string) {
	// An isolated X server is the only X11 cpak runs, and on Wayland it
	// needs both clipboard directions to work at all.
	t.override.DisplayX11 = true
	t.override.Clipboard = types.ClipboardGrant{HostToApp: true, AppToHost: true}
	t.add(request, Narrowed, "the application gets its own X server, never the host display")
}

var xdgDirectories = map[string]bool{
	"xdg-desktop": true, "xdg-documents": true, "xdg-download": true, "xdg-music": true,
	"xdg-pictures": true, "xdg-public-share": true, "xdg-templates": true, "xdg-videos": true,
}

var homeSubdirectories = map[string]string{
	"xdg-config": ".config",
	"xdg-data":   ".local/share",
	"xdg-cache":  ".cache",
}

func (t *translator) filesystem(value string) {
	request := "filesystems=" + value
	if strings.HasPrefix(value, "!") {
		return
	}
	location, mode, _ := strings.Cut(value, ":")
	access := "read-write"
	switch mode {
	case "", "rw", "create":
	case "ro":
		access = "read-only"
	default:
		t.add(request, Refused, "unknown access mode")
		return
	}
	grant := func(target string) {
		t.override.Filesystem = append(t.override.Filesystem, types.FilesystemPermission{Path: target, Access: access})
	}

	head, rest, _ := strings.Cut(location, "/")
	switch {
	case location == "home" || location == "~":
		grant("home")
		t.add(request, Granted, "")
	case location == "host" || location == "host-os" || location == "host-etc" || location == "host-root":
		t.add(request, Refused, "the host root is never shared with a Flatpak application; grant the folders it needs instead")
	case xdgDirectories[location]:
		grant(location)
		t.add(request, Granted, "")
	case xdgDirectories[head]:
		t.add(request, Refused, "cpak grants the "+head+" folder only as a whole; grant it with an override if the application needs it")
	case homeSubdirectories[head] != "":
		if !safeRelative(rest) {
			t.add(request, Refused, "invalid path")
			return
		}
		grant(path.Join("home", homeSubdirectories[head], rest))
		t.add(request, Granted, "")
	case head == "xdg-run":
		t.add(request, Refused, "other services' runtime sockets are never shared")
	case head == "~":
		if !safeRelative(rest) {
			t.add(request, Refused, "invalid path")
			return
		}
		grant(path.Join("home", rest))
		t.add(request, Granted, "")
	case strings.HasPrefix(location, "/"):
		clean := path.Clean(location)
		if clean == "/" || !safeAbsolute(clean) {
			t.add(request, Refused, "that path is never shared")
			return
		}
		grant(clean)
		t.add(request, Granted, "")
	default:
		t.add(request, Refused, "no cpak equivalent")
	}
}

// safeAbsolute keeps the trees cpak and the system run out of a grant before
// the shared validator sees it, so a Flatpak application's request is
// refused in its own words rather than failing the whole install.
func safeAbsolute(location string) bool {
	for _, root := range []string{"/proc", "/sys", "/dev", "/run", "/etc", "/usr", "/boot", "/var/lib", "/var/run", "/root", "/tmp", "/var/tmp", "/app"} {
		if location == root || strings.HasPrefix(location, root+"/") {
			return location == "/run/media" || strings.HasPrefix(location, "/run/media/")
		}
	}
	return true
}

func safeRelative(value string) bool {
	if value == "" {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, "/")
}

func (t *translator) sessionBus(policy map[string]string) {
	for _, name := range sortedKeys(policy) {
		access := policy[name]
		request := "session-bus " + access + " " + name
		if access == "none" || access == "see" {
			continue
		}
		if handled(name) {
			t.add(request, Provided, "cpak answers this service inside the sandbox")
			continue
		}
		if permission, ok := sessionNames[name]; ok && access == "talk" {
			if permission == "notification" {
				t.override.Notification = true
			}
			t.add(request, Granted, "delivered as a cpak "+permission+" host action")
			continue
		}
		if access == "own" {
			candidate := append(append([]string{}, t.override.SessionBus.Own...), name)
			if types.ValidateDBusPolicy(types.DBusPolicy{Own: candidate}) == nil && ownable(name) {
				t.override.SessionBus.Own = candidate
				t.add(request, Granted, "")
				continue
			}
			t.add(request, Refused, "that name cannot be owned")
			continue
		}
		t.add(request, Refused, "cpak does not open whole session bus services; it grants exact calls, which an override can add")
	}
}

func handled(name string) bool {
	for _, prefix := range handledNames {
		if name == strings.TrimSuffix(prefix, ".") || strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// ownable keeps an application from claiming a desktop service name: what it
// may own is a name of its own or a media player name, which is all an owned
// name is for in a Flatpak application.
func ownable(name string) bool {
	if strings.HasPrefix(name, "org.freedesktop.") || strings.HasPrefix(name, "org.gnome.") || strings.HasPrefix(name, "org.kde.") || strings.HasPrefix(name, "org.gtk.") {
		return false
	}
	return true
}

func loaderVariable(name string) bool {
	return strings.HasPrefix(name, "LD_") || name == "GCONV_PATH" || name == "GIO_MODULE_DIR" || name == "GTK_MODULES" || name == "GTK_PATH"
}

func foldFilesystem(permissions []types.FilesystemPermission) []types.FilesystemPermission {
	widest := map[string]string{}
	order := []string{}
	for _, permission := range permissions {
		current, seen := widest[permission.Path]
		if !seen {
			order = append(order, permission.Path)
		}
		if !seen || current == "read-only" {
			widest[permission.Path] = permission.Access
		}
	}
	folded := make([]types.FilesystemPermission, 0, len(order))
	for _, location := range order {
		folded = append(folded, types.FilesystemPermission{Path: location, Access: widest[location]})
	}
	return folded
}

func set(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		if strings.HasPrefix(value, "!") {
			delete(result, strings.TrimPrefix(value, "!"))
			continue
		}
		result[value] = true
	}
	return result
}

func sorted(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
