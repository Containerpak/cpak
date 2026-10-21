/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"time"

	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// flatpakUpdateRequest asks the remote an installation came from for the
// current build of the same ref.
func flatpakUpdateRequest(app types.Application) (FlatpakRequest, error) {
	ref, err := flatpak.ParseRef(app.Flatpak.Ref)
	if err != nil {
		return FlatpakRequest{}, err
	}
	return FlatpakRequest{ID: ref.ID, Branch: ref.Branch, Arch: ref.Arch, Remote: app.Flatpak.Remote}, nil
}

// preflightFlatpakUpdate weighs an update of a Flatpak application the way a
// manifest update is weighed: the policy the new build translates to against
// the one the installation holds. A runtime is never updated on its own; it
// moves with the applications that run on it.
func (c *Cpak) preflightFlatpakUpdate(app types.Application) (types.UpdateResult, *FlatpakPlan) {
	result := types.UpdateResult{
		Origin:     app.Origin,
		Name:       app.Name,
		SourceType: app.SourceType(),
		OldVersion: app.Version,
		NewVersion: app.Version,
	}
	if app.PulledIn {
		result.Status = types.UpdateStatusUpToDate
		result.Reason = "updated with the applications that run on it"
		return result, nil
	}
	request, err := flatpakUpdateRequest(app)
	if err != nil {
		return failedUpdate(result, err), nil
	}
	plan, err := c.ResolveFlatpak(request)
	if err != nil {
		return failedUpdate(result, err), nil
	}
	stored := installedRecordOverride(app)
	result.PermissionChanges = stored.Diff(plan.Translation.Override)
	result.PermissionAdditions = stored.Additions(plan.Translation.Override)
	return result, plan
}

// updateFlatpakApplication moves an installation to the current build of its
// ref, its runtime and their extensions. Every commit already in the store is
// reused, so an update that changed nothing downloads nothing and ends as
// up to date.
func (c *Cpak) updateFlatpakApplication(app types.Application, deps updateDeps, approvedAdditions map[string]bool) types.UpdateResult {
	result := types.UpdateResult{
		Origin:     app.Origin,
		Name:       app.Name,
		SourceType: app.SourceType(),
		OldVersion: app.Version,
		NewVersion: app.Version,
	}
	request, err := flatpakUpdateRequest(app)
	if err != nil {
		return failedUpdate(result, err)
	}
	plan, err := c.ResolveFlatpak(request)
	if err != nil {
		return failedUpdate(result, err)
	}
	stored := installedRecordOverride(app)
	result.PermissionChanges = stored.Diff(plan.Translation.Override)
	result.PermissionAdditions = stored.Additions(plan.Translation.Override)
	for _, addition := range result.PermissionAdditions {
		if !approvedAdditions[addition] {
			result.Status = types.UpdateStatusPermissionDenied
			result.Reason = "additional permissions were not approved"
			return result
		}
	}

	runtime, err := c.updateFlatpakRuntime(plan, app)
	if err != nil {
		return failedUpdate(result, err)
	}
	updated, err := c.flatpakApplicationRecord(plan, runtime)
	if err != nil {
		return failedUpdate(result, err)
	}
	updated.CreatedAt = app.CreatedAt
	updated.UpdatedAt = time.Now()
	if deps.prepareStorage != nil {
		if err = deps.prepareStorage(updated); err != nil {
			return failedUpdate(result, err)
		}
	}
	if sameInstallation(app, updated) {
		if err = deps.createExports(updated); err != nil {
			return failedUpdate(result, err)
		}
		c.EnrolPublishedApplication(updated, PublishedPackage{})
		result.Status = types.UpdateStatusUpToDate
		return result
	}

	transaction, err := c.beginUpdateTransaction(app, updated)
	if err != nil {
		return failedUpdate(result, err)
	}
	if err = deps.createExports(updated); err != nil {
		restoreExports(app, updated, deps)
		return failedUpdate(result, err)
	}
	if err = deps.stop(app); err != nil {
		restoreExports(app, updated, deps)
		return failedUpdate(result, err)
	}
	if err = c.recordRollbackHistory(app); err != nil {
		restoreExports(app, updated, deps)
		return failedUpdate(result, err)
	}
	if err = c.replaceApplication(app, updated); err != nil {
		restoreExports(app, updated, deps)
		return failedUpdate(result, err)
	}
	if err = c.commitUpdateTransaction(transaction); err != nil {
		return failedUpdate(result, err)
	}
	if err = deps.removeExports(app, updated); err != nil {
		logger.Printf("Warning: could not remove the stale exports of %s: %v", app.Name, err)
	}
	if err = c.finishUpdateTransaction(transaction); err != nil {
		return failedUpdate(result, err)
	}
	c.EnrolPublishedApplication(updated, PublishedPackage{})
	result.Status = types.UpdateStatusUpdated
	result.NewVersion = updated.Version
	return result
}

// updateFlatpakRuntime brings the runtime an application needs to the build
// the remote publishes now. A runtime on another branch is installed beside
// the old one, which stays for as long as something else runs on it; the same
// branch is replaced in place, since every application on it is meant to move
// with it.
func (c *Cpak) updateFlatpakRuntime(plan *FlatpakPlan, app types.Application) (types.Application, error) {
	ref := plan.Runtime.Ref
	origin := FlatpakOrigin(plan.Remote.Name, ref.ID)
	existing, _ := c.getStoredApplication(origin, ref.Branch, ref.Branch, "", "")
	if existing.CpakId == "" {
		return c.installFlatpakRuntime(plan)
	}
	layers, extensions, err := c.importFlatpakParts(plan, plan.Runtime, plan.RuntimeExtensions)
	if err != nil {
		return types.Application{}, err
	}
	commit := plan.Runtime.Commit.Checksum.String()
	if existing.Flatpak != nil && existing.Flatpak.Commit == commit && sameLayers(existing.ParsedLayers, layers) {
		return existing, nil
	}
	updated := existing
	updated.ParsedLayers = layers
	updated.ImageDigest = "sha256:" + commit
	updated.UpdatedAt = time.Now()
	source := *existing.Flatpak
	source.Commit = commit
	source.Extensions = extensions
	updated.Flatpak = &source
	if err := c.PrepareApplicationStorage(updated); err != nil {
		return types.Application{}, err
	}
	if err := c.replaceApplication(existing, updated); err != nil {
		return types.Application{}, fmt.Errorf("update the runtime %s: %w", ref.ID, err)
	}
	c.EnrolPublishedApplication(updated, PublishedPackage{})
	logger.Printf("Updated the runtime %s", ref.ID)
	return updated, nil
}

func sameLayers(first, second []string) bool {
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
