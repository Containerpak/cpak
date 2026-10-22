/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// CheckUpdates reports which installed applications have a newer build
// published, without applying anything: no transaction is recovered, no
// export is refreshed and nothing is downloaded beyond what describes the
// builds. An application that has one is reported as available, with the
// permissions the update would add.
func (c *Cpak) CheckUpdates(origin string) ([]types.UpdateResult, error) {
	return c.checkUpdates(origin, c.newUpdateDeps())
}

func (c *Cpak) checkUpdates(origin string, deps updateDeps) ([]types.UpdateResult, error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}
	origin = strings.ToLower(origin)
	results := []types.UpdateResult{}
	for _, app := range apps {
		if origin != "" && strings.ToLower(app.Origin) != origin {
			continue
		}
		if app.Flatpak != nil {
			results = append(results, c.checkFlatpakUpdate(app))
			continue
		}
		results = append(results, c.checkUpdate(app, deps))
	}
	if origin != "" && len(results) == 0 {
		return nil, fmt.Errorf("application not installed: %s", origin)
	}
	return results, nil
}

// checkUpdate answers for a package installed from a Git repository. A new
// release, or a manifest naming another image, is a new build. A branch whose
// manifest still names the same image tag cannot be told apart from an
// unchanged one without pulling it, so it is reported as such rather than
// guessed.
func (c *Cpak) checkUpdate(app types.Application, deps updateDeps) types.UpdateResult {
	result := c.preflightUpdate(app, deps)
	if result.Status != "" {
		return result
	}
	if result.SourceType == "release" {
		result.Status = types.UpdateStatusAvailable
		return result
	}
	manifest, err := deps.fetchManifest(app.Origin, app.Branch, app.Release, "")
	if err != nil {
		return failedUpdate(result, err)
	}
	image, err := resolveManifestImage(manifest, app.Branch, app.Release, "")
	if err != nil {
		return failedUpdate(result, err)
	}
	switch {
	case image != app.Image:
		result.Status = types.UpdateStatusAvailable
	case strings.Contains(image, "@sha256:"):
		result.Status = types.UpdateStatusUpToDate
	default:
		result.Status = types.UpdateStatusUnsupported
		result.Reason = "the image is tracked by tag; only an update can tell whether it moved"
	}
	return result
}

// checkFlatpakUpdate compares the commits an installation was made of with the
// ones its remote publishes now, for the application, its runtime and the
// extensions of both.
func (c *Cpak) checkFlatpakUpdate(app types.Application) types.UpdateResult {
	result, plan := c.preflightFlatpakUpdate(app)
	if plan == nil {
		return result
	}
	result.Status = types.UpdateStatusUpToDate
	if flatpakBuildChanged(app.Flatpak, plan.App, plan.AppExtensions) || app.Flatpak.Runtime != plan.Runtime.Ref.String() {
		result.Status = types.UpdateStatusAvailable
		return result
	}
	runtime, err := c.getStoredApplication(FlatpakOrigin(plan.Remote.Name, plan.Runtime.Ref.ID), plan.Runtime.Ref.Branch, plan.Runtime.Ref.Branch, "", "")
	if err != nil || runtime.Flatpak == nil || flatpakBuildChanged(runtime.Flatpak, plan.Runtime, plan.RuntimeExtensions) {
		result.Status = types.UpdateStatusAvailable
	}
	return result
}

func flatpakBuildChanged(stored *types.FlatpakSource, main FlatpakPart, extensions []FlatpakPart) bool {
	if stored.Commit != main.Commit.Checksum.String() || len(stored.Extensions) != len(extensions) {
		return true
	}
	installed := make(map[string]string, len(stored.Extensions))
	for _, extension := range stored.Extensions {
		installed[extension.Ref] = extension.Commit
	}
	for _, extension := range extensions {
		if installed[extension.Ref.String()] != extension.Commit.Checksum.String() {
			return true
		}
	}
	return false
}
