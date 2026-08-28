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
	parent types.Application
	child  types.Application
	policy launchPolicy
	binary string
}

func (c *Cpak) authorizeNestedRun(params types.RequestParams) (authorizedNestedRun, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return authorizedNestedRun{}, err
	}
	defer store.Close()

	// Which application is calling is resolved from the capability it holds,
	// never from anything it said. The identifier it used to send is public
	// metadata and proved nothing.
	parent, err := parentForNestedToken(store, params.Token)
	if err != nil {
		return authorizedNestedRun{}, err
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

	return authorizedNestedRun{
		parent: parent,
		child:  child,
		policy: narrowedTo(resolvedOverride(child), resolvedOverride(parent)),
		binary: binary,
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

// launchPolicy carries the two answers a launch needs about itself: the policy
// it is recognised by, which is the one an anchor was taken over, and the
// policy it actually runs under, which may be narrower than that.
//
// They are the same thing for an application started on its own terms. They
// part company whenever something narrows a launch after the application was
// enrolled: a nested run is held to the intersection with the package that
// asked for it, and a package the user never named is held to the intersection
// with the packages that pulled it in.
//
// Keeping them apart is what makes narrowing possible at all. The gate derives
// the launch root from the policy it is handed and compares it with the one the
// ledger recorded, so a narrower policy shown to the gate is not a narrower
// launch, it is a launch no anchor names, and pkg/cpak/verify.go refuses that
// at every enforcement level. Narrowing what is mounted is safe; narrowing what
// the gate is shown means the launch never happens.
type launchPolicy struct {
	enrolled  types.Override
	effective types.Override
}

// asLaunched is the policy of an application started on the terms it was
// enrolled with, where there is nothing to tell the two halves apart.
func asLaunched(override types.Override) launchPolicy {
	return launchPolicy{enrolled: override, effective: override}
}

// narrowedTo is the policy of a launch something else has a say in. The
// intersection is computed here rather than taken from the caller, so that the
// half the container is built from cannot be wider than the half the gate
// recognised, whoever is asking.
func narrowedTo(enrolled, ceiling types.Override) launchPolicy {
	return launchPolicy{enrolled: enrolled, effective: intersectOverrides(ceiling, enrolled)}
}

// standaloneLaunchPolicy answers with the policy an application runs under when
// it is launched on its own rather than by a package that depends on it.
//
// For a package the user never named that is not the policy its publisher asked
// for. Such a package is installed whole and can be started directly, and the
// user agreed to it only as part of the package that pulled it in, so it is
// held to the intersection with that package, exactly as a nested run of it
// already is. Without that a publisher reaches past the permissions their own
// package was granted by naming a wider dependency and letting it be launched
// by itself.
//
// A package the user named is untouched, whoever else declares it, and so is a
// package declared by somebody who did not bring it here. Otherwise any
// publisher could narrow software they have nothing to do with simply by
// naming it in a manifest.
//
// When the package that brought it here is gone the launch is bounded by
// nothing, which is the same answer cpak gives a dependency nobody declares any
// more: what is left is an installation with no relationship to anything, and
// deciding what to do with those is garbage collection, not consent.
func standaloneLaunchPolicy(store *Store, app types.Application) (launchPolicy, error) {
	policy := asLaunched(resolvedOverride(app))
	if !app.PulledIn {
		return policy, nil
	}
	installed, err := store.GetApplications()
	if err != nil {
		return launchPolicy{}, fmt.Errorf("read the installed applications: %w", err)
	}
	for _, parent := range installed {
		if parent.CpakId == app.CpakId || !boundsTheLaunchOf(parent, app) {
			continue
		}
		policy.effective = intersectOverrides(resolvedOverride(parent), policy.effective)
	}
	return policy, nil
}

// boundsTheLaunchOf answers whether the given installation has a say in how the
// application starts on its own. Declaring an origin is not enough: the
// installer records which package an installation came here behind, and only
// that one is speaking about something the user agreed to.
//
// A record that names nobody was written before cpak kept that answer, and
// there every package that declares the application is taken to have a say,
// which is the narrower of the two readings.
func boundsTheLaunchOf(parent, app types.Application) bool {
	if !declaresDependency(parent, app) {
		return false
	}
	return app.PulledInBy == "" || parent.Origin == app.PulledInBy
}

// declaresDependency answers whether the parent asked for the given
// application. The origin is checked beside the identifier the installer
// recorded, the way an authorized nested run checks a child, so that an
// identifier which came to name something else cannot pass for a declaration
// the parent never made.
//
// The mode is deliberately not checked. A nested run is authorized only for a
// nested dependency, but this is a different question: what the user agreed to
// when they installed the parent covers everything the parent asked for, and a
// publisher must not be able to put a wide package beyond reach of the
// intersection by declaring it as a layer.
func declaresDependency(parent, app types.Application) bool {
	for _, dependency := range parent.ParsedDependencies {
		if dependency.Id == app.CpakId && dependency.Origin == app.Origin {
			return true
		}
	}
	return false
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
		FilePicker:          intersectFilePicker(parent.FilePicker, child.FilePicker),
		SessionBus:          types.IntersectDBusPolicies(parent.SessionBus, child.SessionBus),
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
