/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Install installs a package from a given origin. The origin must be a git
// repository with a valid cpak manifest file in the root directory.
// The branch, release and commit parameters are used to select the version of
// the package to install. Note that those parameters are mutually exclusive,
// the installation will fail if more than one of them is specified.
//
// Note: this function is not meant to be used by final clients, which should
// likely implement their own installers, calling the FetchManifest and
// InstallCpak functions instead, that way they can implement their own
// installation logic, by showing more detailed information to the user.
func (c *Cpak) Install(origin, branch, release, commit string) (err error) {
	return c.InstallWithOptions(origin, branch, release, commit, InstallOptions{CreateExports: true})
}

// InstallOptions controls package exports and lock file use.
type InstallOptions struct {
	CreateExports bool
	ManifestLock  *types.ManifestLock
}

// InstallWithOptions installs a remote package with explicit options.
func (c *Cpak) InstallWithOptions(origin, branch, release, commit string, options InstallOptions) (err error) {
	origin = strings.ToLower(origin)

	versionParams := []string{branch, release, commit}
	versionParamsCount := 0
	for _, versionParam := range versionParams {
		if versionParam != "" {
			versionParamsCount++
		}
	}
	if versionParamsCount > 1 {
		return fmt.Errorf("more than one version parameter specified")
	}

	// if all version parameters are empty, we default to the main branch
	// assuming it is the default branch of the repository
	if versionParamsCount == 0 {
		branch = "main"
	}

	var manifest *types.CpakManifest
	if locked, ok := lockedPackageFromManifestLock(options.ManifestLock, origin, branch, release, commit); ok {
		copy := *locked.Manifest
		copy.Image = locked.ResolvedImage
		manifest = &copy
	} else {
		manifest, err = c.FetchManifest(origin, branch, release, commit)
		if err != nil {
			return err
		}
	}

	return c.InstallCpakWithOptions(origin, manifest, branch, commit, release, options)
}

// InstallCpak installs a package from a given manifest file.
//
// Note: this function can be used to install packages from a local manifest
// but this behaviour is not fully supported yet.
func (c *Cpak) InstallCpak(origin string, manifest *types.CpakManifest, branch string, commit string, release string) (err error) {
	return c.InstallCpakWithOptions(origin, manifest, branch, commit, release, InstallOptions{CreateExports: true})
}

// InstallCpakWithOptions installs a decoded manifest with explicit options.
func (c *Cpak) InstallCpakWithOptions(origin string, manifest *types.CpakManifest, branch string, commit string, release string, options InstallOptions) (err error) {
	err = c.ValidateManifest(manifest)
	if err != nil {
		return
	}

	var version string
	var sourceType string
	switch {
	case branch != "":
		sourceType = "branch"
		if commit != "" {
			version = commit
		} else {
			version = branch
		}
	case release != "":
		sourceType = "release"
		version = release
	case commit != "":
		sourceType = "commit"
		version = commit
	}

	existingApp, _ := c.getStoredApplication(origin, version, branch, commit, release)
	if existingApp.CpakId != "" {
		logger.Println("application already installed, perform an Audit if this application is not working as expected")
		return
	}

	// first we resolve its dependencies
	parsedManifestDependencies, err := c.installDependenciesWithOptions(origin, manifest, options)
	if err != nil {
		return
	}

	imageIdBase := manifest.Name + ":" + sourceType + ":" + version + ":" + origin
	cpakImageId := base64.StdEncoding.EncodeToString([]byte(imageIdBase))

	layers, config, imageDigest, err := c.Pull(manifest.Image, cpakImageId)
	if err != nil {
		return
	}
	layers, err = c.BuildRuntimeLayers(layers, manifest.RuntimeSources)
	if err != nil {
		return
	}

	app := types.Application{
		CpakId:               cpakImageId,
		Name:                 manifest.Name,
		Version:              version,
		Origin:               origin,
		Branch:               branch,
		Release:              release,
		Commit:               commit,
		InstallTimestamp:     time.Now(),
		ParsedBinaries:       manifest.Binaries,
		ParsedDesktopEntries: manifest.DesktopEntries,
		ParsedDependencies:   parsedManifestDependencies,
		ParsedAddons:         manifest.Addons,
		IdleTime:             manifest.IdleTime,
		ParsedLayers:         layers,
		RuntimeSources:       manifest.RuntimeSources,
		Config:               config,
		Image:                manifest.Image,
		ImageDigest:          imageDigest,
		ParsedOverride:       manifest.Override,
	}

	if options.CreateExports {
		err = c.createExports(app)
		if err != nil {
			return
		}
	}

	err = c.storeApplication(app)
	if err != nil {
		return
	}

	return nil
}

// installDependencies installs the dependencies declared in the given manifest
// and returns them in their parsed form.
//
// Note: the store is opened only once every dependency has been installed,
// since it cannot be opened twice at the same time.
func (c *Cpak) installDependencies(origin string, manifest *types.CpakManifest) (dependencies []types.Dependency, err error) {
	return c.installDependenciesWithOptions(origin, manifest, InstallOptions{CreateExports: true})
}

func (c *Cpak) installDependenciesWithOptions(origin string, manifest *types.CpakManifest, options InstallOptions) (dependencies []types.Dependency, err error) {
	if len(manifest.Dependencies) == 0 {
		return nil, nil
	}

	refs := []types.Dependency{}
	for _, depManifest := range manifest.Dependencies {
		depOrigin := depManifest.Origin
		if !isURL(depOrigin) {
			logger.Printf("dependency %s is not a valid cpak url, assuming it comes from the same origin", depOrigin)
			parentOrigin := origin[:strings.LastIndex(origin, "/")]
			depOrigin = parentOrigin + "/" + depOrigin
		}

		branch, release, commit := dependencySelectors(depManifest)
		var errInstallDep error
		if locked, ok := lockedPackageFromManifestLock(options.ManifestLock, depOrigin, branch, release, commit); ok {
			lockedManifest := *locked.Manifest
			lockedManifest.Image = locked.ResolvedImage
			errInstallDep = c.InstallCpakWithOptions(depOrigin, &lockedManifest, branch, commit, release, options)
		} else if options.ManifestLock != nil {
			errInstallDep = fmt.Errorf("dependency is missing from lock: %s", depOrigin)
		} else {
			errInstallDep = c.InstallWithOptions(depOrigin, branch, release, commit, options)
		}
		if errInstallDep != nil {
			return nil, fmt.Errorf("failed to install dependency %s: %w", depOrigin, errInstallDep)
		}

		refs = append(refs, types.Dependency{
			Origin:  depOrigin,
			Branch:  branch,
			Release: release,
			Commit:  commit,
		})
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	for _, ref := range refs {
		installedDepApp, errGetDep := findInstalledDependency(store, ref)
		if errGetDep != nil || installedDepApp.CpakId == "" {
			return nil, fmt.Errorf("failed to retrieve installed dependency %s after installation attempt: %w", ref.Origin, errGetDep)
		}
		dependencies = append(dependencies, types.Dependency{
			Id:      installedDepApp.CpakId,
			Origin:  installedDepApp.Origin,
			Branch:  installedDepApp.Branch,
			Release: installedDepApp.Release,
			Commit:  installedDepApp.Commit,
		})
	}

	return dependencies, nil
}

// dependencySelectors returns the selectors of a dependency, which are
// mutually exclusive, defaulting to the main branch.
func dependencySelectors(dependency types.Dependency) (branch, release, commit string) {
	switch {
	case dependency.Release != "":
		return "", dependency.Release, ""
	case dependency.Commit != "":
		return "", "", dependency.Commit
	case dependency.Branch != "":
		return dependency.Branch, "", ""
	}
	return "main", "", ""
}

// findInstalledDependency returns the stored application matching the
// selectors of the given dependency.
func findInstalledDependency(store *Store, dependency types.Dependency) (types.Application, error) {
	return store.GetApplicationByOrigin(dependency.Origin, "", dependency.Branch, dependency.Commit, dependency.Release)
}

func isURL(s string) bool {
	return len(s) > 3 && (strings.HasPrefix(s, "http") || strings.Contains(s, "/"))
}

// createExports creates the exports for a given application.
func (c *Cpak) createExports(app types.Application) (err error) {
	for _, entry := range app.ParsedDesktopEntries {
		var exportErr error
		for i := len(app.ParsedLayers) - 1; i >= 0; i-- {
			layer := app.ParsedLayers[i]
			layerDir := c.GetInStoreDir("layers", layer)
			exportErr = c.exportDesktopEntry(layerDir, app, entry)
			if exportErr == nil {
				break
			}
		}
		if exportErr != nil {
			return exportErr
		}
	}

	for _, binary := range app.ParsedBinaries {
		err = c.exportBinary(app, binary)
		if err != nil {
			return
		}
	}
	return
}

// exportDesktopEntry exports a desktop entry to the user's home directory
// it also exports the icon defined in the desktop entry. If the icon is not
// an absolute path, it looks for it in the common directories, preferring the
// one with the highest resolution.
func (c *Cpak) exportDesktopEntry(rootFs string, app types.Application, desktopEntry string) error {
	home := os.Getenv("HOME")

	var originalPath string
	entryBase := filepath.Base(desktopEntry)
	direct := filepath.Join(rootFs, strings.TrimLeft(desktopEntry, "/"))
	if _, err := os.Stat(direct); err == nil {
		originalPath = direct
	} else {
		_ = filepath.Walk(rootFs, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if filepath.Base(path) == entryBase {
				originalPath = path
				return filepath.SkipDir
			}
			return nil
		})
	}
	if originalPath == "" {
		return fmt.Errorf("desktop entry %s not found under %s", entryBase, rootFs)
	}

	desktopDir := filepath.Join(home, ".local", "share", "applications", app.CpakId)
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		return err
	}
	desktopDest := filepath.Join(desktopDir, entryBase)

	data, err := os.ReadFile(originalPath)
	if err != nil {
		return err
	}
	content := string(data)

	var iconName string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "Icon=") {
			iconName = strings.TrimPrefix(line, "Icon=")
			break
		}
	}
	if iconName == "" {
		return nil
	}

	var absIconPath string
	for i := len(app.ParsedLayers) - 1; i >= 0; i-- {
		layer := app.ParsedLayers[i]
		layerDir := c.GetInStoreDir("layers", layer)
		if iconPath := findIcon(layerDir, iconName); iconPath != "" {
			absIconPath = iconPath
			break
		}
	}

	if absIconPath == "" && filepath.IsAbs(iconName) {
		if _, err := os.Stat(iconName); err == nil {
			absIconPath = iconName
		}
	}

	if absIconPath != "" {
		ext := filepath.Ext(absIconPath)
		iconDest := filepath.Join(os.Getenv("HOME"), ".local", "share", "icons", app.CpakId+ext)
		if err := os.MkdirAll(filepath.Dir(iconDest), 0755); err != nil {
			return err
		}
		if err := tools.CopyFile(absIconPath, iconDest); err != nil {
			return err
		}
		logger.Printf("Exported icon to %s", iconDest)
		iconName = iconDest
	} else {
		logger.Printf("Warning: icon %s not found for app %s", iconName, app.Name)
	}

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Exec=") {
			cmdPart := strings.TrimPrefix(line, "Exec=")
			lines[i] = "Exec=cpak run " + app.Origin + " @" + cmdPart
		}
		if strings.HasPrefix(line, "Icon=") && iconName != "" {
			lines[i] = "Icon=" + iconName
		}
	}
	newContent := strings.Join(lines, "\n")
	return os.WriteFile(desktopDest, []byte(newContent), 0755)
}

func findIcon(layerDir, iconName string) string {
	if filepath.IsAbs(iconName) {
		candidate := filepath.Join(layerDir, iconName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return ""
	}

	var iconPath string
	iconScore := -1
	_ = filepath.Walk(layerDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		name := info.Name()
		extension := strings.ToLower(filepath.Ext(name))
		if extension != ".png" && extension != ".svg" && extension != ".xpm" {
			return nil
		}
		if name != iconName && strings.TrimSuffix(name, filepath.Ext(name)) != iconName {
			return nil
		}

		score := 0
		if extension == ".svg" {
			score = 1000000
		}
		resolution := filepath.Base(filepath.Dir(filepath.Dir(path)))
		var width, height int
		if _, err := fmt.Sscanf(resolution, "%dx%d", &width, &height); err == nil {
			score += min(width, height)
		}
		if score > iconScore {
			iconPath = path
			iconScore = score
		}
		return nil
	})
	return iconPath
}

func (c *Cpak) exportBinary(app types.Application, binary string) error {
	destinationItems := []string{c.Options.ExportsPath}
	destinationItems = append(destinationItems, strings.Split(app.Origin, "/")...)
	destinationItems = append(destinationItems, filepath.Base(binary))
	destinationPath := filepath.Join(destinationItems...)

	err := os.MkdirAll(filepath.Dir(destinationPath), 0755)
	if err != nil {
		return err
	}

	scriptContent := fmt.Sprintf("#!/bin/sh\ncpak run %s @%s -- \"$@\"\n", app.Origin, binary)
	err = os.WriteFile(destinationPath, []byte(scriptContent), 0755)
	if err != nil {
		return err
	}
	return nil
}

// Remove removes a package from the local store, including all the containers
// and exports associated with it. It also removes the application and
// container files from the cpak data directory.
func (c *Cpak) Remove(origin string, branch string, commit string, release string) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	appToRemove, err := store.GetApplicationByOrigin(origin, "", branch, commit, release)
	if err != nil || appToRemove.CpakId == "" {
		_ = store.Close()
		return fmt.Errorf("application %s not found for specified criteria: %w", origin, err)
	}
	if err = store.Close(); err != nil {
		return
	}
	users, err := c.addonUsers(appToRemove.Origin)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		origins := make([]string, 0, len(users))
		for _, user := range users {
			origins = append(origins, user.Origin)
		}
		return fmt.Errorf("application %s is enabled as an addon by %s", origin, strings.Join(origins, ", "))
	}

	// Stop all containers associated with the application
	err = c.Stop(appToRemove.Origin, appToRemove.Version, appToRemove.Branch, appToRemove.Commit, appToRemove.Release)
	if err != nil {
		return fmt.Errorf("failed to stop containers for %s: %w", appToRemove.Name, err)
	}

	store, err = NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	switch {
	case branch != "":
		err = store.RemoveApplicationByOriginAndBranch(origin, branch)
	case commit != "":
		err = store.RemoveApplicationByOriginAndCommit(origin, commit)
	case release != "":
		err = store.RemoveApplicationByOriginAndRelease(origin, release)
	default:
		return fmt.Errorf("no remote (branch, commit or release) specified for removal logic")
	}

	if err != nil {
		_ = store.Close()
		return fmt.Errorf("failed to remove application from store: %w", err)
	}
	if err = store.Close(); err != nil {
		return
	}

	err = c.removeExports(appToRemove)
	if err != nil {
		logger.Printf("Warning: failed to remove all exports for %s: %v", appToRemove.Name, err)
	}
	if err = removeAddonConfiguration(appToRemove); err != nil {
		return fmt.Errorf("remove addon configuration for %s: %w", appToRemove.Name, err)
	}
	if err = c.clearRollbackHistory(origin); err != nil {
		return fmt.Errorf("remove rollback history for %s: %w", appToRemove.Name, err)
	}

	// an Audit is needed to remove resources (containers, exports, etc.)
	// which are not used anymore
	err = c.Audit(true)
	if err != nil {
		return
	}
	return
}

func (c *Cpak) removeExports(app types.Application) error {
	home := os.Getenv("HOME")

	desktopDir := filepath.Join(home, ".local", "share", "applications", app.CpakId)
	if err := os.RemoveAll(desktopDir); err != nil {
		logger.Printf("Warning: could not remove desktop entries dir %s: %v", desktopDir, err)
	}

	iconsDir := filepath.Join(home, ".local", "share", "icons")
	entries, err := os.ReadDir(iconsDir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, app.CpakId+".") {
				path := filepath.Join(iconsDir, name)
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					logger.Printf("Warning: could not remove icon %s: %v", path, err)
				}
			}
		}
	}

	for _, binary := range app.ParsedBinaries {
		if err := c.removeBinaryExport(app.Origin, filepath.Base(binary)); err != nil {
			logger.Printf("Warning: could not remove binary export %s: %v", binary, err)
		}
	}

	return nil
}

// removeBinaryExport removes an exported binary and prunes the directories
// left empty by its removal.
func (c *Cpak) removeBinaryExport(origin, name string) error {
	destinationItems := []string{c.Options.ExportsPath}
	destinationItems = append(destinationItems, strings.Split(origin, "/")...)
	destinationItems = append(destinationItems, name)
	destinationPath := filepath.Join(destinationItems...)

	if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(destinationPath)
	for dir != c.Options.ExportsPath && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}

	return nil
}

// removeStaleExports removes the exports of a replaced application which are
// not provided anymore by the one that took its place.
func (c *Cpak) removeStaleExports(old types.Application, updated types.Application) error {
	home := os.Getenv("HOME")

	if old.CpakId != updated.CpakId {
		desktopDir := filepath.Join(home, ".local", "share", "applications", old.CpakId)
		if err := os.RemoveAll(desktopDir); err != nil {
			return err
		}

		iconsDir := filepath.Join(home, ".local", "share", "icons")
		entries, err := os.ReadDir(iconsDir)
		if err == nil {
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), old.CpakId+".") {
					continue
				}
				if err := os.Remove(filepath.Join(iconsDir, entry.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	} else {
		keptEntries := make(map[string]bool, len(updated.ParsedDesktopEntries))
		for _, entry := range updated.ParsedDesktopEntries {
			keptEntries[filepath.Base(entry)] = true
		}

		desktopDir := filepath.Join(home, ".local", "share", "applications", old.CpakId)
		for _, entry := range old.ParsedDesktopEntries {
			name := filepath.Base(entry)
			if keptEntries[name] {
				continue
			}
			if err := os.Remove(filepath.Join(desktopDir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}

	kept := map[string]bool{}
	for _, binary := range updated.ParsedBinaries {
		kept[filepath.Base(binary)] = true
	}

	for _, binary := range old.ParsedBinaries {
		name := filepath.Base(binary)
		if kept[name] {
			continue
		}
		if err := c.removeBinaryExport(old.Origin, name); err != nil {
			return err
		}
	}

	return nil
}
