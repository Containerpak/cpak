/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"reflect"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Intersect answers the policy both sides allow.
//
// It is the only way a policy is ever narrowed, and that is deliberate: a
// parent restricting a nested child, an administrator restricting a host, and a
// user restricting their own application are the same operation, so none of
// them can drift into a second set of rules that disagrees with the others.
// Every boolean is an and, every list is a smaller list, and every limit is the
// tighter of the two.
func Intersect(parent, child types.Override, h Host) types.Override {
	return types.Override{
		SocketX11:           parent.SocketX11 && child.SocketX11,
		DisplayX11:          parent.DisplayX11 && child.DisplayX11,
		SocketWayland:       parent.SocketWayland && child.SocketWayland,
		SocketPulseAudio:    parent.SocketPulseAudio && child.SocketPulseAudio,
		SocketSessionBus:    parent.SocketSessionBus && child.SocketSessionBus,
		SocketSystemBus:     parent.SocketSystemBus && child.SocketSystemBus,
		SocketSshAgent:      parent.SocketSshAgent && child.SocketSshAgent,
		SocketCups:          parent.SocketCups && child.SocketCups,
		SocketGpgAgent:      parent.SocketGpgAgent && child.SocketGpgAgent,
		SocketAtSpiBus:      parent.SocketAtSpiBus && child.SocketAtSpiBus,
		SocketBluetooth:     parent.SocketBluetooth && child.SocketBluetooth,
		Bluetooth:           parent.Bluetooth && child.Bluetooth,
		DeviceDri:           parent.DeviceDri && child.DeviceDri,
		DeviceKvm:           parent.DeviceKvm && child.DeviceKvm,
		DeviceShm:           parent.DeviceShm && child.DeviceShm,
		DeviceAlsa:          parent.DeviceAlsa && child.DeviceAlsa,
		DeviceVideo:         parent.DeviceVideo && child.DeviceVideo,
		DeviceFuse:          parent.DeviceFuse && child.DeviceFuse,
		DeviceTun:           parent.DeviceTun && child.DeviceTun,
		DeviceUsb:           parent.DeviceUsb && child.DeviceUsb,
		DeviceSerial:        parent.DeviceSerial && child.DeviceSerial,
		DeviceInput:         parent.DeviceInput && child.DeviceInput,
		DeviceTTY:           parent.DeviceTTY && child.DeviceTTY,
		DeviceAll:           parent.DeviceAll && child.DeviceAll,
		Notification:        parent.Notification && child.Notification,
		OpenURI:             parent.OpenURI && child.OpenURI,
		HostApplications:    parent.HostApplications && child.HostApplications,
		HostActions:         types.IntersectHostActions(parent.HostActions, child.HostActions),
		FilePicker:          IntersectFilePicker(parent.FilePicker, child.FilePicker),
		SessionBus:          types.IntersectDBusPolicies(parent.SessionBus, child.SessionBus),
		Filesystem:          IntersectFilesystem(parent.Filesystem, child.Filesystem, h),
		FsHost:              parent.FsHost && child.FsHost,
		FsHostEtc:           parent.FsHostEtc && child.FsHostEtc,
		FsHostHome:          parent.FsHostHome && child.FsHostHome,
		FsExtra:             IntersectStrings(parent.FsExtra, child.FsExtra),
		Env:                 IntersectStrings(parent.Env, child.Env),
		Network:             parent.Network && child.Network,
		HostNetwork:         parent.HostNetwork && child.HostNetwork,
		Process:             parent.Process && child.Process,
		UserNamespaces:      parent.UserNamespaces && child.UserNamespaces,
		MemoryMaxMB:         MinimumLimit(parent.MemoryMaxMB, child.MemoryMaxMB),
		CPUQuota:            MinimumLimit(parent.CPUQuota, child.CPUQuota),
		PidsMax:             MinimumLimit(parent.PidsMax, child.PidsMax),
		AsRoot:              parent.AsRoot && child.AsRoot,
		AllowedHostCommands: nil,
	}
}

// IntersectFilePicker narrows the file chooser the same way, one capability at
// a time.
func IntersectFilePicker(parent, child types.FilePickerGrant) types.FilePickerGrant {
	return types.FilePickerGrant{
		OpenFile:         parent.OpenFile && child.OpenFile,
		OpenFolder:       parent.OpenFolder && child.OpenFolder,
		SaveFile:         parent.SaveFile && child.SaveFile,
		Persistent:       parent.Persistent && child.Persistent,
		ContainingFolder: parent.ContainingFolder && child.ContainingFolder,
	}
}

// IntersectFilesystem keeps a requested permission only where the granting side
// already covers the same place, and never at a wider access than either side
// allows. A path nobody granted is dropped rather than downgraded.
func IntersectFilesystem(parent, child []types.FilesystemPermission, h Host) []types.FilesystemPermission {
	permissions := make(map[string]string, len(child))
	for _, requested := range child {
		access := ""
		for _, granted := range parent {
			if !FilesystemContains(granted.Path, requested.Path, h) {
				continue
			}
			candidate := requested.Access
			if granted.Access == "read-only" || requested.Access == "read-only" {
				candidate = "read-only"
			}
			if access == "" || candidate == "read-only" {
				access = candidate
			}
		}
		if access != "" {
			permissions[requested.Path] = access
		}
	}
	paths := make([]string, 0, len(permissions))
	for path := range permissions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]types.FilesystemPermission, 0, len(paths))
	for _, path := range paths {
		result = append(result, types.FilesystemPermission{Path: path, Access: permissions[path]})
	}
	return result
}

// FilesystemContains reports whether a granted scope covers a requested one.
//
// The portable scopes are compared as themselves where they can be, and against
// the home directory where one side wrote a real path. A host with no home to
// resolve answers no, because a comparison that cannot be made must not read as
// a grant.
func FilesystemContains(parent, child string, h Host) bool {
	if parent == "host" {
		return true
	}
	if child == "host" {
		return parent == "host"
	}
	if parent == "home" && child == "home" {
		return true
	}
	if parent == "home" {
		if h.Home == "" {
			return false
		}
		parent = h.Home
	}
	if child == "home" {
		if h.Home == "" {
			return false
		}
		child = h.Home
	}
	return parent == child || strings.HasPrefix(child, parent+"/")
}

// MinimumLimit answers the tighter of two resource limits, where zero means no
// limit and therefore loses to any number.
func MinimumLimit(parent, child int) int {
	if parent == 0 {
		return child
	}
	if child == 0 || parent < child {
		return parent
	}
	return child
}

// IntersectStrings keeps the values the child asked for that the parent already
// allows, in the order the child wrote them.
func IntersectStrings(parent, child []string) []string {
	allowed := make(map[string]struct{}, len(parent))
	for _, value := range parent {
		allowed[value] = struct{}{}
	}
	result := make([]string, 0, len(child))
	for _, value := range child {
		if _, ok := allowed[value]; ok {
			result = append(result, value)
		}
	}
	return result
}

// Ceiling is the widest policy a host permits.
//
// Present says whether an administrator has decided anything at all: a host
// with no ceiling is unmanaged and every application keeps the policy it
// already had. Named is what the administrator actually wrote about, and a
// nil Named means all of them, which is the reading for a ceiling assembled in
// code rather than read from a file. The empty map is a different case and a
// real one: a file that names nothing constrains nothing.
type Ceiling struct {
	Present bool
	Policy  types.Override
	Named   map[string]bool
}

// UnderCeiling holds a policy to the widest one this host permits. The manifest
// asks and the owner of the application may narrow it further, but neither can
// reach past what the administrator decided, and no signature changes that: who
// published an application is a different question from how much it is allowed
// to do here.
func UnderCeiling(requested types.Override, ceiling Ceiling, h Host) types.Override {
	if !ceiling.Present {
		return requested
	}
	return HeldToNamed(requested, Intersect(ceiling.Policy, requested, h), ceiling.Named)
}

// HeldToNamed keeps the intersected value for every permission the ceiling
// names and the requested one for the rest.
//
// The intersection is computed whole so it stays the same operation nested
// packages use, and this decides only which of its fields the administrator
// asked about. Without it a ceiling would answer for permissions it never
// mentioned, and the answer would be whatever the zero value happened to be:
// no filesystem, no environment, no host actions, for everything on the host.
func HeldToNamed(requested, restricted types.Override, named map[string]bool) types.Override {
	if named == nil {
		return restricted
	}
	named = PermissionsReached(named)
	result := requested
	target := reflect.ValueOf(&result).Elem()
	source := reflect.ValueOf(restricted)
	fields := target.Type()
	for index := 0; index < fields.NumField(); index++ {
		key := strings.Split(fields.Field(index).Tag.Get("json"), ",")[0]
		if key == "" || !named[key] {
			continue
		}
		target.Field(index).Set(source.Field(index))
	}
	return result
}

// AliasFamily is a set of permissions that reach the same thing by another
// name, and the ones a ceiling has to hold as well to mean what it says.
type AliasFamily struct {
	WhenAnyOf []string
	AlsoHold  []string
}

// PermissionAliases groups the permissions that reach the same thing by another
// name. Holding a ceiling to exactly the keys an administrator typed is right
// until two keys open the same door, and then it is a way to close one of them.
//
// The families, and why each is one:
//
//   - every device permission implies deviceAll, which mounts the whole of
//     /dev. An administrator who closes the GPU, KVM, USB and the rest one at a
//     time has named everything except the key that grants all of them.
//   - the legacy v1 filesystem fields mount the same places the typed list
//     does, through an older spelling, so a ceiling over the list closes them
//     too. It does not run the other way: naming one legacy field is a narrow
//     decision and must not wipe every typed permission an application has.
//   - socketBluetooth mounts /run/dbus/system_bus_socket, which is the socket
//     socketSystemBus mounts. They are one permission wearing two names.
//
// Naming deviceAll on its own does not pull the specific device permissions in
// with it: closing the blanket while leaving the GPU open is a policy somebody
// might mean, and the reverse never is.
var PermissionAliases = []AliasFamily{
	{
		WhenAnyOf: []string{
			"deviceDri", "deviceKvm", "deviceShm", "deviceAlsa", "deviceVideo",
			"deviceFuse", "deviceTun", "deviceUsb", "deviceSerial", "deviceInput",
			"deviceTTY",
		},
		AlsoHold: []string{"deviceAll"},
	},
	{
		WhenAnyOf: []string{"filesystem"},
		AlsoHold:  []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"},
	},
	{
		WhenAnyOf: []string{"socketSystemBus", "socketBluetooth"},
		AlsoHold:  []string{"socketSystemBus", "socketBluetooth"},
	},
}

// PermissionsReached answers with the permissions a ceiling reaches, which is
// the ones it names plus the ones that would undo them.
func PermissionsReached(named map[string]bool) map[string]bool {
	reached := make(map[string]bool, len(named))
	for key := range named {
		reached[key] = true
	}
	for _, family := range PermissionAliases {
		for _, trigger := range family.WhenAnyOf {
			if !named[trigger] {
				continue
			}
			for _, key := range family.AlsoHold {
				reached[key] = true
			}
			break
		}
	}
	return reached
}
