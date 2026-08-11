/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// GetInstalledApps returns a list of installed applications.
//
// Note: this function should always be called after the Audit function
// to ensure that the store is in a consistent state.
func (c *Cpak) GetInstalledApps() (apps []types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open store for GetInstalledApps: %w", err)
	}
	defer store.Close()

	apps, err = store.GetApplications()
	if err != nil {
		return nil, fmt.Errorf("failed to get applications from store: %w", err)
	}
	return
}

// getStoredApplication returns the stored application matching the given
// criteria, if any.
func (c *Cpak) getStoredApplication(origin, version, branch, commit, release string) (app types.Application, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return app, err
	}
	defer store.Close()

	return store.GetApplicationByOrigin(origin, version, branch, commit, release)
}

// storeApplication saves the given application in the store.
func (c *Cpak) storeApplication(app types.Application) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()

	return store.NewApplication(app)
}

// updateDeps groups the operations an update performs outside of the store,
// so that tests can replace them.
type updateDeps struct {
	latestRelease func(origin string) (string, error)
	fetchManifest func(origin, branch, release, commit string) (*types.CpakManifest, error)
	installDeps   func(origin string, manifest *types.CpakManifest) ([]types.Dependency, error)
	pull          func(image, cpakImageId string) ([]string, string, string, error)
	buildRuntime  func(layers []string, sources []types.RuntimeSource) ([]string, error)
	stop          func(app types.Application) error
	createExports func(app types.Application) error
	removeExports func(old types.Application, updated types.Application) error
}

func (c *Cpak) newUpdateDeps() updateDeps {
	return updateDeps{
		latestRelease: c.GetLatestRelease,
		fetchManifest: c.FetchManifest,
		installDeps:   c.installDependencies,
		pull:          c.Pull,
		buildRuntime:  c.BuildRuntimeLayers,
		stop:          c.stopApplicationContainers,
		createExports: c.createExports,
		removeExports: c.removeStaleExports,
	}
}

// Update updates the installed application matching the given origin, or every
// installed application when the origin is empty. Each application is updated
// according to how it was installed and its outcome is returned as a structured
// result, so that the caller can format it.
func (c *Cpak) Update(origin string) (results []types.UpdateResult, err error) {
	return c.update(origin, c.newUpdateDeps())
}

func (c *Cpak) update(origin string, deps updateDeps) (results []types.UpdateResult, err error) {
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}

	origin = strings.ToLower(origin)
	selected := []types.Application{}
	for _, app := range apps {
		if origin != "" && strings.ToLower(app.Origin) != origin {
			continue
		}
		selected = append(selected, app)
	}

	if origin != "" && len(selected) == 0 {
		return nil, fmt.Errorf("application not installed: %s", origin)
	}

	results = []types.UpdateResult{}
	for _, app := range selected {
		results = append(results, c.updateApplication(app, deps))
	}

	return results, nil
}

// updateApplication updates a single installed application, restoring the
// previous store record and exports whenever a step fails.
func (c *Cpak) updateApplication(app types.Application, deps updateDeps) (result types.UpdateResult) {
	result = types.UpdateResult{
		Origin:     app.Origin,
		Name:       app.Name,
		SourceType: app.SourceType(),
		OldVersion: app.Version,
		NewVersion: app.Version,
	}

	// moving an application bound to a commit would mean picking a version
	// the user never asked for
	if app.Commit != "" {
		result.Status = types.UpdateStatusPinned
		result.Reason = "installed from an immutable commit"
		return result
	}

	branch := app.Branch
	release := app.Release

	switch result.SourceType {
	case "branch":
	case "release":
		latest, errLatest := deps.latestRelease(app.Origin)
		if errLatest != nil {
			if errors.Is(errLatest, ErrLatestReleaseUnsupported) {
				result.Status = types.UpdateStatusUnsupported
				result.Reason = errLatest.Error()
				return result
			}
			return failedUpdate(result, errLatest)
		}
		if latest == app.Release {
			result.Status = types.UpdateStatusUpToDate
			return result
		}
		release = latest
	default:
		result.Status = types.UpdateStatusUnsupported
		result.Reason = "unknown source type"
		return result
	}

	manifest, err := deps.fetchManifest(app.Origin, branch, release, "")
	if err != nil {
		return failedUpdate(result, err)
	}

	if err = c.ValidateManifest(manifest); err != nil {
		return failedUpdate(result, err)
	}

	dependencies, err := deps.installDeps(app.Origin, manifest)
	if err != nil {
		return failedUpdate(result, err)
	}

	version := branch
	if release != "" {
		version = release
	}
	imageIdBase := manifest.Name + ":" + result.SourceType + ":" + version + ":" + app.Origin
	cpakImageId := base64.StdEncoding.EncodeToString([]byte(imageIdBase))

	layers, config, imageDigest, err := deps.pull(manifest.Image, cpakImageId)
	if err != nil {
		return failedUpdate(result, err)
	}
	layers, err = deps.buildRuntime(layers, manifest.RuntimeSources)
	if err != nil {
		return failedUpdate(result, err)
	}

	updated := types.Application{
		CpakId:               cpakImageId,
		Name:                 manifest.Name,
		Version:              version,
		Origin:               app.Origin,
		Branch:               branch,
		Release:              release,
		CreatedAt:            app.CreatedAt,
		UpdatedAt:            time.Now(),
		InstallTimestamp:     time.Now(),
		ParsedBinaries:       manifest.Binaries,
		ParsedDesktopEntries: manifest.DesktopEntries,
		ParsedDependencies:   dependencies,
		ParsedAddons:         manifest.Addons,
		IdleTime:             manifest.IdleTime,
		ParsedLayers:         layers,
		RuntimeSources:       manifest.RuntimeSources,
		Config:               config,
		Image:                manifest.Image,
		ImageDigest:          imageDigest,
		ParsedOverride:       manifest.Override,
	}
	result.PermissionChanges = app.ParsedOverride.Diff(updated.ParsedOverride)

	if sameInstallation(app, updated) {
		if err = deps.createExports(updated); err != nil {
			return failedUpdate(result, err)
		}
		if app.ImageDigest == "" {
			if err = c.replaceApplication(app, updated); err != nil {
				return failedUpdate(result, err)
			}
		}
		result.Status = types.UpdateStatusUpToDate
		return result
	}

	transaction, err := c.beginUpdateTransaction(app, updated)
	if err != nil {
		return failedUpdate(result, err)
	}

	// the new installation is complete, the old one can be replaced
	if err = deps.createExports(updated); err != nil {
		restoreExports(app, updated, deps)
		return failedUpdate(result, err)
	}

	if err = deps.stop(app); err != nil {
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

	result.Status = types.UpdateStatusUpdated
	result.NewVersion = updated.Version
	return result
}

// restoreExports rolls the exports back to the application which was not
// replaced after all.
func restoreExports(app types.Application, updated types.Application, deps updateDeps) {
	if err := deps.removeExports(updated, app); err != nil {
		logger.Printf("Warning: could not remove the exports of the aborted update of %s: %v", app.Name, err)
	}
	if err := deps.createExports(app); err != nil {
		logger.Printf("Warning: could not restore the exports of %s: %v", app.Name, err)
	}
}

// replaceApplication stores the updated application in place of the one it
// replaces. The old record is dropped first and restored on failure, so that
// the store never holds both.
func (c *Cpak) replaceApplication(app types.Application, updated types.Application) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()

	if app.CpakId == updated.CpakId {
		return store.NewApplication(updated)
	}

	if err = store.RemoveApplicationByCpakId(app.CpakId); err != nil {
		return err
	}

	if err = store.NewApplication(updated); err != nil {
		if restoreErr := store.NewApplication(app); restoreErr != nil {
			return fmt.Errorf("failed to store %s and to restore %s: %w", updated.CpakId, app.CpakId, restoreErr)
		}
		return err
	}

	return nil
}

// stopApplicationContainers stops and cleans up every container of the given
// application.
//
// Note: the store is closed before the cleanup, since it cannot be opened
// twice at the same time.
func (c *Cpak) stopApplicationContainers(app types.Application) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}

	containers, err := store.GetApplicationContainers(app)
	closeErr := store.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	for _, container := range containers {
		pid := container.Pid
		if pid == 0 {
			pid, _ = getPidFromEnvContainerId(container.CpakId)
		}
		if pid != 0 {
			logger.Println("Stopping container process:", pid)
			syscall.Kill(pid, syscall.SIGTERM)
		}
		if cleanupErr := c.CleanupContainer(container); cleanupErr != nil {
			logger.Printf("Warning: error during container cleanup %s: %v", container.CpakId, cleanupErr)
		}
	}

	return nil
}

// sameInstallation reports whether the resolved application is the one already
// installed, meaning there is nothing to replace.
func sameInstallation(app types.Application, updated types.Application) bool {
	if app.CpakId != updated.CpakId || app.Version != updated.Version || app.Config != updated.Config {
		return false
	}
	if app.Image != "" && app.Image != updated.Image {
		return false
	}
	if app.ImageDigest != "" && app.ImageDigest != updated.ImageDigest {
		return false
	}
	if !reflect.DeepEqual(app.ParsedLayers, updated.ParsedLayers) {
		return false
	}
	if !reflect.DeepEqual(app.ParsedBinaries, updated.ParsedBinaries) {
		return false
	}
	if !reflect.DeepEqual(app.ParsedDesktopEntries, updated.ParsedDesktopEntries) {
		return false
	}
	if !reflect.DeepEqual(app.ParsedAddons, updated.ParsedAddons) {
		return false
	}
	if app.IdleTime != updated.IdleTime {
		return false
	}
	if !reflect.DeepEqual(app.ParsedDependencies, updated.ParsedDependencies) {
		return false
	}
	if !reflect.DeepEqual(app.RuntimeSources, updated.RuntimeSources) {
		return false
	}
	return reflect.DeepEqual(app.ParsedOverride, updated.ParsedOverride)
}

func failedUpdate(result types.UpdateResult, err error) types.UpdateResult {
	result.Status = types.UpdateStatusFailed
	result.Reason = err.Error()
	result.Err = err
	return result
}
