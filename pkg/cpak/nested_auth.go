/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type authorizedNestedRun struct {
	parent   types.Application
	child    types.Application
	override types.Override
	binary   string
}

func (c *Cpak) authorizeNestedRun(params types.RequestParams) (authorizedNestedRun, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return authorizedNestedRun{}, err
	}
	defer store.Close()

	parent, err := store.GetApplicationByCpakId(params.ParentAppId)
	if err != nil {
		return authorizedNestedRun{}, fmt.Errorf("parent application not found: %w", err)
	}

	dependency, err := declaredDependency(parent, params)
	if err != nil {
		return authorizedNestedRun{}, err
	}
	child, err := store.GetApplicationByCpakId(dependency.Id)
	if err != nil {
		return authorizedNestedRun{}, fmt.Errorf("declared dependency is not installed: %w", err)
	}
	if child.Origin != dependency.Origin {
		return authorizedNestedRun{}, fmt.Errorf("declared dependency does not match the installed application")
	}

	binary, err := exportedNestedBinary(child, params.Binary)
	if err != nil {
		return authorizedNestedRun{}, err
	}

	parentOverride := resolvedOverride(parent)
	childOverride := resolvedOverride(child)
	return authorizedNestedRun{
		parent:   parent,
		child:    child,
		override: intersectOverrides(parentOverride, childOverride),
		binary:   binary,
	}, nil
}

func declaredDependency(parent types.Application, params types.RequestParams) (types.Dependency, error) {
	matches := make([]types.Dependency, 0, 1)
	for _, dependency := range parent.ParsedDependencies {
		if !dependency.IsNested() {
			continue
		}
		if dependency.Origin != params.Origin {
			continue
		}
		if params.Branch != "" && dependency.Branch != params.Branch {
			continue
		}
		if params.Commit != "" && dependency.Commit != params.Commit {
			continue
		}
		if params.Release != "" && dependency.Release != params.Release {
			continue
		}
		matches = append(matches, dependency)
	}
	if len(matches) == 0 {
		return types.Dependency{}, fmt.Errorf("%s is not a declared dependency of %s", params.Origin, parent.Name)
	}
	if len(matches) > 1 {
		return types.Dependency{}, fmt.Errorf("dependency %s is ambiguous", params.Origin)
	}
	return matches[0], nil
}

func exportedNestedBinary(app types.Application, requested string) (string, error) {
	requested = strings.TrimPrefix(requested, "@")
	for _, binary := range app.ParsedBinaries {
		if requested == binary || filepath.Base(requested) == filepath.Base(binary) {
			return binary, nil
		}
	}
	return "", fmt.Errorf("binary %s is not exported by %s", requested, app.Name)
}

func resolvedOverride(app types.Application) types.Override {
	return underHostCeiling(requestedOverride(app))
}

// requestedOverride answers with what the owner of the application decided, and
// with the manifest when they decided nothing.
//
// Whether they decided anything is answered by the file being there, not by
// comparing what it says to the defaults. That comparison was harmless while
// the defaults granted things, because a policy identical to them was one
// nobody had narrowed. Now that they grant nothing it would read the most
// restrictive choice an owner can make, deny everything, as no choice at all,
// and hand the application whatever its manifest asked for instead.
func requestedOverride(app types.Application) types.Override {
	userOverride, err := LoadOverride(app.Origin, app.Version)
	if err != nil {
		return app.ParsedOverride
	}
	return userOverride
}

// underHostCeiling holds a policy to the widest one this host permits. The
// manifest asks and the owner of the application may narrow it further, but
// neither can reach past what the administrator decided, and no signature
// changes that: who published an application is a different question from how
// much it is allowed to do here.
//
// The intersection is the one nested packages already use, so a ceiling
// restricts exactly the way a parent restricts a child, rather than by a second
// set of rules that would drift from it.
func underHostCeiling(requested types.Override) types.Override {
	ceiling := hostCeiling()
	if !ceiling.Present {
		return requested
	}
	return heldToNamed(requested, intersectOverrides(ceiling.Policy, requested), ceiling.Named)
}

// heldToNamed keeps the intersected value for every permission the ceiling
// names and the requested one for the rest.
//
// The intersection is computed whole so it stays the same operation nested
// packages use, and this decides only which of its fields the administrator
// asked about. Without it a ceiling would answer for permissions it never
// mentioned, and the answer would be whatever the zero value happened to be:
// no filesystem, no environment, no host actions, for everything on the host.
//
// A nil map means the ceiling speaks for all of them. That is the safe reading
// for a ceiling that did not come from a file, and it is not the same as a file
// that named nothing, which is an empty map and holds nobody to anything.
func heldToNamed(requested, restricted types.Override, named map[string]bool) types.Override {
	if named == nil {
		return restricted
	}
	named = withAliases(named)
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

var hostCeiling = func() systemauthority.Ceiling { return systemauthority.HostCeiling() }

func intersectOverrides(parent, child types.Override) types.Override {
	return types.Override{
		SocketX11:           parent.SocketX11 && child.SocketX11,
		SocketWayland:       parent.SocketWayland && child.SocketWayland,
		SocketPulseAudio:    parent.SocketPulseAudio && child.SocketPulseAudio,
		SocketSessionBus:    parent.SocketSessionBus && child.SocketSessionBus,
		SocketSystemBus:     parent.SocketSystemBus && child.SocketSystemBus,
		SocketSshAgent:      parent.SocketSshAgent && child.SocketSshAgent,
		SocketCups:          parent.SocketCups && child.SocketCups,
		SocketGpgAgent:      parent.SocketGpgAgent && child.SocketGpgAgent,
		SocketAtSpiBus:      parent.SocketAtSpiBus && child.SocketAtSpiBus,
		SocketBluetooth:     parent.SocketBluetooth && child.SocketBluetooth,
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
		FilePicker:          intersectFilePicker(parent.FilePicker, child.FilePicker),
		Filesystem:          intersectFilesystem(parent.Filesystem, child.Filesystem),
		FsHost:              parent.FsHost && child.FsHost,
		FsHostEtc:           parent.FsHostEtc && child.FsHostEtc,
		FsHostHome:          parent.FsHostHome && child.FsHostHome,
		FsExtra:             intersectStrings(parent.FsExtra, child.FsExtra),
		Env:                 intersectStrings(parent.Env, child.Env),
		Network:             parent.Network && child.Network,
		Process:             parent.Process && child.Process,
		UserNamespaces:      parent.UserNamespaces && child.UserNamespaces,
		MemoryMaxMB:         minimumLimit(parent.MemoryMaxMB, child.MemoryMaxMB),
		CPUQuota:            minimumLimit(parent.CPUQuota, child.CPUQuota),
		PidsMax:             minimumLimit(parent.PidsMax, child.PidsMax),
		AsRoot:              parent.AsRoot && child.AsRoot,
		AllowedHostCommands: nil,
	}
}

func intersectFilePicker(parent, child types.FilePickerGrant) types.FilePickerGrant {
	return types.FilePickerGrant{
		OpenFile:         parent.OpenFile && child.OpenFile,
		OpenFolder:       parent.OpenFolder && child.OpenFolder,
		SaveFile:         parent.SaveFile && child.SaveFile,
		Persistent:       parent.Persistent && child.Persistent,
		ContainingFolder: parent.ContainingFolder && child.ContainingFolder,
	}
}

func intersectFilesystem(parent, child []types.FilesystemPermission) []types.FilesystemPermission {
	permissions := make(map[string]string, len(child))
	for _, requested := range child {
		access := ""
		for _, granted := range parent {
			if !filesystemContains(granted.Path, requested.Path) {
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

func filesystemContains(parent, child string) bool {
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
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		parent = home
	}
	if child == "home" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		child = home
	}
	return parent == child || strings.HasPrefix(child, parent+"/")
}

func minimumLimit(parent, child int) int {
	if parent == 0 {
		return child
	}
	if child == 0 || parent < child {
		return parent
	}
	return child
}

func intersectStrings(parent, child []string) []string {
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

// permissionAliases groups the permissions that reach the same thing by another
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
var permissionAliases = []struct {
	whenAnyOf []string
	alsoHold  []string
}{
	{
		whenAnyOf: []string{
			"deviceDri", "deviceKvm", "deviceShm", "deviceAlsa", "deviceVideo",
			"deviceFuse", "deviceTun", "deviceUsb", "deviceSerial", "deviceInput",
			"deviceTTY",
		},
		alsoHold: []string{"deviceAll"},
	},
	{
		whenAnyOf: []string{"filesystem"},
		alsoHold:  []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"},
	},
	{
		whenAnyOf: []string{"socketSystemBus", "socketBluetooth"},
		alsoHold:  []string{"socketSystemBus", "socketBluetooth"},
	},
}

// withAliases answers with the permissions a ceiling reaches, which is the ones
// it names plus the ones that would undo them.
func withAliases(named map[string]bool) map[string]bool {
	reached := make(map[string]bool, len(named))
	for key := range named {
		reached[key] = true
	}
	for _, family := range permissionAliases {
		for _, trigger := range family.whenAnyOf {
			if !named[trigger] {
				continue
			}
			for _, key := range family.alsoHold {
				reached[key] = true
			}
			break
		}
	}
	return reached
}
