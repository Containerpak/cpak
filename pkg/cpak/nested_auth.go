/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

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
	userOverride, err := LoadOverride(app.Origin, app.Version)
	if err == nil && !reflect.DeepEqual(userOverride, types.NewOverride()) {
		return userOverride
	}
	return app.ParsedOverride
}

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
		DeviceAll:           parent.DeviceAll && child.DeviceAll,
		Notification:        parent.Notification && child.Notification,
		OpenURI:             parent.OpenURI && child.OpenURI,
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
		AllowedHostCommands: intersectStrings(parent.AllowedHostCommands, child.AllowedHostCommands),
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
