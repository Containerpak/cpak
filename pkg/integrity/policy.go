/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	PolicySchemaWithoutSerial     = 1
	PolicySchemaWithoutSessionBus = 2
	CurrentPolicySchema           = 3
)

// PolicyRoot hashes what an application is allowed to do. Lists whose order
// carries no meaning are sorted first; the environment is left as it stands
// because a later assignment wins over an earlier one.
func PolicyRoot(override types.Override) (string, error) {
	return PolicyRootForSchema(override, CurrentPolicySchema)
}

// PolicyRootForSchema derives a root using the policy shape recorded in an
// enrolment. New policies always use CurrentPolicySchema.
func PolicyRootForSchema(override types.Override, schema int) (string, error) {
	canonical := canonicalPolicy(override)
	switch schema {
	case PolicySchemaWithoutSerial:
		if canonical.DeviceSerial {
			return "", errors.New("serial devices are not part of this policy schema")
		}
		if canonical.SessionBus.Enabled() {
			return "", errors.New("session bus rules are not part of this policy schema")
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			return "", err
		}
		withoutSerial := bytes.Replace(encoded, []byte(`,"deviceSerial":false`), nil, 1)
		if len(withoutSerial) == len(encoded) {
			return "", errors.New("serial device field is missing from the current policy schema")
		}
		withoutSessionBus := bytes.Replace(withoutSerial, []byte(`,"sessionBus":{}`), nil, 1)
		if len(withoutSessionBus) == len(withoutSerial) {
			return "", errors.New("session bus field is missing from the current policy schema")
		}
		return digestJSON("policy", withoutSessionBus), nil
	case PolicySchemaWithoutSessionBus:
		if canonical.SessionBus.Enabled() {
			return "", errors.New("session bus rules are not part of this policy schema")
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			return "", err
		}
		withoutSessionBus := bytes.Replace(encoded, []byte(`,"sessionBus":{}`), nil, 1)
		if len(withoutSessionBus) == len(encoded) {
			return "", errors.New("session bus field is missing from the current policy schema")
		}
		return digestJSON("policy", withoutSessionBus), nil
	case CurrentPolicySchema:
		return digest("policy", canonical)
	default:
		return "", fmt.Errorf("unsupported policy schema %d", schema)
	}
}

func canonicalPolicy(override types.Override) types.Override {
	canonical := override
	canonical.Filesystem = sortedPermissions(override.Filesystem)
	canonical.HostActions = sortedActions(override.HostActions)
	canonical.SessionBus = types.CanonicalDBusPolicy(override.SessionBus)
	canonical.FsExtra = sorted(override.FsExtra)
	canonical.AllowedHostCommands = sorted(override.AllowedHostCommands)
	return canonical
}

// Restricts reports whether candidate grants no more than current. Two policies
// that cannot be ordered are not a restriction: the safe answer is that the
// owner has to be asked, so an unrecognised difference always widens.
//
// Both sides are read with their filesystem grants in one form first. A v1
// package was enrolled under fsHostHome and is installed again under
// {path: home, access: read-write}, and those are one restriction written twice:
// asked in the two shapes, no held grant covers the wanted one, the pair cannot
// be ordered, and cpak asks for an administrator password because it changed
// the shape of its own manifest. Nobody should be asked that question, and an
// owner asked it for nothing is an owner who stops reading it.
//
// PolicyRoot deliberately does not do this. What is hashed is the policy as it
// stands, so that an anchor names a value the gate can derive again; what is
// ordered is what the policy means, which is this.
func Restricts(current, candidate types.Override) bool {
	current = current.WithMigratedFilesystem()
	candidate = candidate.WithMigratedFilesystem()
	for _, pair := range [][2]bool{
		{candidate.SocketX11, current.SocketX11},
		{candidate.SocketWayland, current.SocketWayland},
		{candidate.SocketPulseAudio, current.SocketPulseAudio},
		{candidate.SocketSessionBus, current.SocketSessionBus},
		{candidate.SocketSystemBus, current.SocketSystemBus},
		{candidate.SocketSshAgent, current.SocketSshAgent},
		{candidate.SocketCups, current.SocketCups},
		{candidate.SocketGpgAgent, current.SocketGpgAgent},
		{candidate.SocketAtSpiBus, current.SocketAtSpiBus},
		{candidate.SocketBluetooth, current.SocketBluetooth},
		{candidate.DeviceDri, current.DeviceDri},
		{candidate.DeviceKvm, current.DeviceKvm},
		{candidate.DeviceShm, current.DeviceShm},
		{candidate.DeviceAlsa, current.DeviceAlsa},
		{candidate.DeviceVideo, current.DeviceVideo},
		{candidate.DeviceFuse, current.DeviceFuse},
		{candidate.DeviceTun, current.DeviceTun},
		{candidate.DeviceUsb, current.DeviceUsb},
		{candidate.DeviceSerial, current.DeviceSerial},
		{candidate.DeviceInput, current.DeviceInput},
		{candidate.DeviceTTY, current.DeviceTTY},
		{candidate.DeviceAll, current.DeviceAll},
		{candidate.Notification, current.Notification},
		{candidate.OpenURI, current.OpenURI},
		{candidate.HostApplications, current.HostApplications},
		{candidate.Network, current.Network},
		{candidate.Process, current.Process},
		{candidate.UserNamespaces, current.UserNamespaces},
		{candidate.AsRoot, current.AsRoot},
		{candidate.FilePicker.OpenFile, current.FilePicker.OpenFile},
		{candidate.FilePicker.OpenFolder, current.FilePicker.OpenFolder},
		{candidate.FilePicker.SaveFile, current.FilePicker.SaveFile},
		{candidate.FilePicker.Persistent, current.FilePicker.Persistent},
		{candidate.FilePicker.ContainingFolder, current.FilePicker.ContainingFolder},
	} {
		if pair[0] && !pair[1] {
			return false
		}
	}
	// A limit of zero means no limit, so it is the widest value there is.
	for _, limit := range [][2]int{
		{candidate.MemoryMaxMB, current.MemoryMaxMB},
		{candidate.CPUQuota, current.CPUQuota},
		{candidate.PidsMax, current.PidsMax},
	} {
		if limit[1] == 0 {
			continue
		}
		if limit[0] == 0 || limit[0] > limit[1] {
			return false
		}
	}
	// fsHost, fsHostEtc, fsHostHome and fsExtra are not compared here. They are
	// filesystem grants written in the older spelling and they were read as
	// such above, which is also what lets a path inside one of them count as
	// the narrowing it is rather than as a name nobody held.
	if !subset(candidate.AllowedHostCommands, current.AllowedHostCommands) {
		return false
	}
	if !actionsCovered(current.HostActions, candidate.HostActions) {
		return false
	}
	if !types.DBusPolicyRestricts(current.SessionBus, candidate.SessionBus) {
		return false
	}
	if !permissionsCovered(current.Filesystem, candidate.Filesystem) {
		return false
	}
	// The environment is not ordered, so any difference counts as widening.
	return equalStrings(current.Env, candidate.Env)
}

func permissionsCovered(current, candidate []types.FilesystemPermission) bool {
	for _, wanted := range candidate {
		covered := false
		for _, held := range current {
			if pathCovers(held.Path, wanted.Path) && accessCovers(held.Access, wanted.Access) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// pathCovers is deliberately literal. A scope that merely looks narrower, such
// as an XDG directory against a home grant, is not treated as covered, because
// where it resolves to is decided elsewhere.
func pathCovers(held, wanted string) bool {
	if held == wanted || held == "host" {
		return true
	}
	return strings.HasPrefix(wanted, strings.TrimSuffix(held, "/")+"/")
}

func accessCovers(held, wanted string) bool {
	if held == wanted {
		return true
	}
	return held == "read-write" && wanted == "read-only"
}

// actionsCovered compares capability by capability: dropping one capability of a
// provider is a restriction, not a different grant.
func actionsCovered(current, candidate []types.HostActionGrant) bool {
	held := map[string]map[string]bool{}
	for _, action := range current {
		set := held[action.Provider]
		if set == nil {
			set = map[string]bool{}
			held[action.Provider] = set
		}
		for _, capability := range action.Capabilities {
			set[capability] = true
		}
	}
	for _, action := range candidate {
		set := held[action.Provider]
		if set == nil {
			return false
		}
		for _, capability := range action.Capabilities {
			if !set[capability] {
				return false
			}
		}
	}
	return true
}

func actionKey(action types.HostActionGrant) string {
	return action.Provider + "\x00" + strings.Join(sorted(action.Capabilities), ",")
}

func sortedPermissions(values []types.FilesystemPermission) []types.FilesystemPermission {
	if len(values) == 0 {
		return nil
	}
	copied := append([]types.FilesystemPermission{}, values...)
	sort.Slice(copied, func(i, j int) bool {
		if copied[i].Path == copied[j].Path {
			return copied[i].Access < copied[j].Access
		}
		return copied[i].Path < copied[j].Path
	})
	return copied
}

func sortedActions(values []types.HostActionGrant) []types.HostActionGrant {
	if len(values) == 0 {
		return nil
	}
	copied := make([]types.HostActionGrant, 0, len(values))
	for _, action := range values {
		action.Capabilities = sorted(action.Capabilities)
		copied = append(copied, action)
	}
	sort.Slice(copied, func(i, j int) bool { return actionKey(copied[i]) < actionKey(copied[j]) })
	return copied
}

func subset(candidate, current []string) bool {
	held := make(map[string]bool, len(current))
	for _, value := range current {
		held[value] = true
	}
	for _, value := range candidate {
		if !held[value] {
			return false
		}
	}
	return true
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
