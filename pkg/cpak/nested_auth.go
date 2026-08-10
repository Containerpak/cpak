/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"fmt"
	"path/filepath"
	"reflect"
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
		FsHost:              parent.FsHost && child.FsHost,
		FsHostEtc:           parent.FsHostEtc && child.FsHostEtc,
		FsHostHome:          parent.FsHostHome && child.FsHostHome,
		FsExtra:             intersectStrings(parent.FsExtra, child.FsExtra),
		Env:                 intersectStrings(parent.Env, child.Env),
		Network:             parent.Network && child.Network,
		Process:             parent.Process && child.Process,
		AsRoot:              parent.AsRoot && child.AsRoot,
		AllowedHostCommands: intersectStrings(parent.AllowedHostCommands, child.AllowedHostCommands),
	}
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
