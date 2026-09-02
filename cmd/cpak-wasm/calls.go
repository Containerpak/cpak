/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/core"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// validateManifest answers whether a manifest means anything, and the first
// thing that stops it if it does not.
//
// One problem is reported rather than all of them, because that is what the
// runtime does: validation stops at the first failure, and a list of everything
// wrong would be a different answer from the one cpak gives.
func validateManifest(payload string) any {
	var request manifestSource
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}
	manifest, err := request.decode()
	if err != nil {
		return success(map[string]any{
			"valid": false,
			"stage": "decode",
			"error": err.Error(),
		})
	}
	result := map[string]any{
		"valid":        true,
		"legacyFields": core.LegacyFilesystemFields(manifest),
	}
	if err := core.ValidateManifest(manifest); err != nil {
		result["valid"] = false
		result["stage"] = "rules"
		result["error"] = err.Error()
	}
	result["manifestVersion"] = manifest.ManifestVersion
	return success(result)
}

// ungrantedPermissions names the permissions a manifest never mentions.
//
// A permission written as false and one nobody wrote both arrive as false in
// the decoded manifest, and the difference is what an author needs told:
// nothing is granted by omission, so silence about the display or the session
// bus is an application that will not have them.
func ungrantedPermissions(payload string) any {
	var request manifestSource
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}
	raw, err := request.bytes()
	if err != nil {
		return failure(err)
	}
	missing, err := types.UngrantedPermissions(raw)
	if err != nil {
		return failure(err)
	}
	return success(map[string]any{"permissions": missing})
}

type policyRequest struct {
	manifestSource
	Override     *types.Override `json:"override,omitempty"`
	UserOverride *types.Override `json:"userOverride,omitempty"`
	Ceiling      json.RawMessage `json:"ceiling,omitempty"`
	Host         *hostRequest    `json:"host,omitempty"`
}

// effectivePolicy answers the policy an application actually runs under and the
// mounts it produces on the described host.
//
// The three parties do not work the same way, and the answer says which one
// decided: the manifest asks, an override by the owner replaces that request
// outright, and the ceiling narrows whatever survived without ever widening it.
func effectivePolicy(payload string) any {
	var request policyRequest
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}

	declared := types.Override{}
	if request.Override != nil {
		declared = *request.Override
	} else {
		manifest, err := request.decode()
		if err != nil {
			return failure(err)
		}
		if err := core.ValidateManifest(manifest); err != nil {
			return failure(err)
		}
		declared = manifest.Override
	}

	ceiling := core.Ceiling{}
	if len(request.Ceiling) > 0 && string(request.Ceiling) != "null" {
		decoded, err := core.DecodeCeiling(request.Ceiling)
		if err != nil {
			return failure(errors.New("the ceiling is not a policy: " + err.Error()))
		}
		ceiling = decoded
	}

	described := defaultHost
	if request.Host != nil {
		described = *request.Host
	}
	host := described.host()

	asked := declared
	if request.UserOverride != nil {
		asked = *request.UserOverride
	}
	effective, source := core.EffectiveOverride(declared, request.UserOverride, ceiling, host)
	mounts, _ := core.OverrideMounts(effective, host)

	result := map[string]any{
		"source":    string(source),
		"requested": asked,
		"effective": effective,
		"narrowed":  asked.Diff(effective),
		"mounts":    mounts,
		"shims":     core.SystemBrokerShims(effective),
		"host":      described,
	}
	if ceiling.Present {
		result["ceilingHolds"] = sortedKeys(core.PermissionsReached(ceiling.Named))
	}
	return success(result)
}

// migrateManifest upgrades a v1 manifest and says what each field became, which
// is the part an author cannot read off the result: a legacy flag does not
// disappear, it turns into a typed permission with an access mode somebody has
// to agree with.
func migrateManifest(payload string) any {
	var request manifestSource
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}
	manifest, err := request.decode()
	if err != nil {
		return failure(err)
	}
	before := manifest.Override
	version := manifest.ManifestVersion
	if err := core.MigrateManifest(manifest); err != nil {
		return failure(err)
	}
	return success(map[string]any{
		"manifest":        manifest,
		"manifestVersion": manifest.ManifestVersion,
		"changes":         migrationChanges(version, manifest.ManifestVersion, before),
	})
}

func migrationChanges(from, to string, before types.Override) []map[string]string {
	changes := []map[string]string{}
	note := func(field, became string) {
		changes = append(changes, map[string]string{"field": field, "became": became})
	}
	if from != to {
		note("manifest_version", to)
	}
	if before.FsHost {
		note("fsHost", "filesystem: host (read-only)")
	}
	if before.FsHostEtc {
		note("fsHostEtc", "filesystem: /etc (read-only)")
	}
	if before.FsHostHome {
		note("fsHostHome", "filesystem: home (read-write)")
	}
	for _, extra := range before.FsExtra {
		note("fsExtra", "filesystem: "+extra+" (read-write)")
	}
	for _, command := range before.AllowedHostCommands {
		switch command {
		case "notify-send":
			note("allowedHostCommands", "notification")
		case "xdg-open":
			note("allowedHostCommands", "openURI")
		case "cpak-launch-app":
			note("allowedHostCommands", "hostApplications")
		}
	}
	return changes
}

type filesystemRequest struct {
	Filesystem []types.FilesystemPermission `json:"filesystem"`
	Host       *hostRequest                 `json:"host,omitempty"`
}

// filesystemPlan answers where every filesystem permission lands on the
// described host: the directory the contents come from, and the path the
// application finds them at.
//
// The pair is the interesting part. It is the same path for everything except
// the host scope, which arrives at /run/host so an application handed the whole
// filesystem cannot mistake it for its own root, and except the portable
// scopes, which name a place on a machine rather than a path and are therefore
// a different directory on every machine.
//
// An entry is resolved on its own and answers its own failure, because one line
// nobody can resolve does not stop the rest. The list is judged separately: a
// path written twice is a list cpak refuses even though both lines are fine.
func filesystemPlan(payload string) any {
	var request filesystemRequest
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}
	described := defaultHost
	if request.Host != nil {
		described = *request.Host
	}
	host := described.host()

	entries := make([]map[string]string, 0, len(request.Filesystem))
	for _, permission := range request.Filesystem {
		entry := map[string]string{
			"path":   permission.Path,
			"access": permission.Access,
		}
		source, target, err := core.ResolveFilesystem(permission, host)
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["source"] = source
			entry["target"] = target
		}
		entries = append(entries, entry)
	}

	result := map[string]any{
		"valid":   true,
		"entries": entries,
		"host":    described,
	}
	if err := types.ValidateFilesystemPermissions(request.Filesystem); err != nil {
		result["valid"] = false
		result["error"] = err.Error()
	}
	return success(result)
}

type desktopRequest struct {
	Entry    string `json:"entry"`
	Name     string `json:"name,omitempty"`
	Launcher string `json:"launcher,omitempty"`
	Origin   string `json:"origin"`
	CpakID   string `json:"cpakId,omitempty"`
	Icon     string `json:"icon,omitempty"`
}

// desktopEntry answers the two files cpak writes for an exported entry.
//
// The exported one is what a menu runs, with the publisher's command replaced
// by a cpak run of the same command inside the sandbox. The alias keeps the
// publisher's own file name so a launcher that was told about the application
// by name still finds it, and carries the keys that say whose it is.
func desktopEntry(payload string) any {
	var request desktopRequest
	if err := decodeRequest(payload, &request); err != nil {
		return failure(err)
	}
	if strings.TrimSpace(request.Entry) == "" {
		return failure(errors.New("no desktop entry was given"))
	}
	if request.Launcher == "" {
		request.Launcher = "/usr/bin/cpak"
	}
	export := core.DesktopExport{
		Launcher: request.Launcher,
		Origin:   request.Origin,
		CpakID:   request.CpakID,
		Icon:     request.Icon,
	}
	exported := core.RewriteDesktopEntry(request.Entry, export)
	name := request.Name
	if name == "" {
		name = "application.desktop"
	}
	return success(map[string]any{
		"exportId":         core.DesktopExportID(request.CpakID),
		"exported":         exported,
		"exportedFileName": core.DesktopExportFileName(request.CpakID, name),
		"alias":            core.DesktopAliasEntry(exported, export),
		"aliasFileName":    path.Base(name),
	})
}

// permissionCatalog answers the permission model of this build, read off the
// type that defines it.
func permissionCatalog(string) any {
	return success(map[string]any{
		"permissions": core.PermissionCatalog(),
		"aliases":     core.PermissionAliases,
	})
}

func success(result any) any {
	return response{OK: true, Result: result}
}

func failure(err error) any {
	return response{OK: false, Error: err.Error()}
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
