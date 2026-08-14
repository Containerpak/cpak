/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
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
	return c.InstallWithOptions(origin, branch, release, commit, InstallOptions{CreateExports: true, ResolveImageRef: true})
}

// InstallOptions controls package exports and lock file use.
type InstallOptions struct {
	CreateExports   bool
	ManifestLock    *types.ManifestLock
	ResolveImageRef bool
}

// InstallWithOptions installs a remote package with explicit options.
func (c *Cpak) InstallWithOptions(origin, branch, release, commit string, options InstallOptions) (err error) {
	origin = strings.ToLower(origin)
	options.ResolveImageRef = true

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

	// If no selector is supplied, follow the default branch declared by the
	// repository host.
	if versionParamsCount == 0 {
		branch, err = c.GetDefaultBranch(origin)
		if err != nil {
			return err
		}
	}

	var manifest *types.CpakManifest
	if locked, ok := lockedPackageFromManifestLock(options.ManifestLock, origin, branch, release, commit); ok {
		copy := *locked.Manifest
		copy.Image = locked.ResolvedImage
		copy.ImageRef = ""
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
	return c.InstallCpakWithOptions(origin, manifest, branch, commit, release, InstallOptions{CreateExports: true, ResolveImageRef: true})
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
		logger.Printf("%s is already installed from %s; run an audit if it is not working as expected", manifest.Name, origin)
		return
	}

	image := manifest.Image
	if options.ResolveImageRef {
		image, err = resolveManifestImage(manifest, branch, release, commit)
		if err != nil {
			return
		}
	}

	// first we resolve its dependencies
	parsedManifestDependencies, err := c.installDependenciesWithOptions(origin, manifest, options)
	if err != nil {
		return
	}

	imageIdBase := manifest.Name + ":" + sourceType + ":" + version + ":" + origin
	cpakImageId := base64.StdEncoding.EncodeToString([]byte(imageIdBase))

	layers, config, imageDigest, err := c.pull(image, cpakImageId, origin)
	if err != nil {
		if errors.Is(err, oci.ErrAuthenticationRequired) {
			return fmt.Errorf("%s requires registry access; run cpak auth login %s", manifest.Name, origin)
		}
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
		ParsedSessions:       manifest.Sessions,
		ParsedDependencies:   parsedManifestDependencies,
		ParsedAddons:         manifest.Addons,
		IdleTime:             manifest.IdleTime,
		ParsedLayers:         layers,
		RuntimeSources:       manifest.RuntimeSources,
		Config:               config,
		Image:                image,
		ImageDigest:          imageDigest,
		ParsedOverride:       manifest.Override,
	}
	if err = c.PrepareApplicationStorage(app); err != nil {
		return
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
	return c.installDependenciesWithOptions(origin, manifest, InstallOptions{CreateExports: true, ResolveImageRef: true})
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
		dependencyOptions := options
		dependencyOptions.ResolveImageRef = true
		if depManifest.IsLayer() {
			dependencyOptions.CreateExports = false
		}
		var errInstallDep error
		if locked, ok := lockedPackageFromManifestLock(options.ManifestLock, depOrigin, branch, release, commit); ok {
			lockedManifest := *locked.Manifest
			lockedManifest.Image = locked.ResolvedImage
			lockedManifest.ImageRef = ""
			errInstallDep = c.InstallCpakWithOptions(depOrigin, &lockedManifest, branch, commit, release, dependencyOptions)
		} else if options.ManifestLock != nil {
			errInstallDep = fmt.Errorf("dependency is missing from lock: %s", depOrigin)
		} else {
			errInstallDep = c.InstallWithOptions(depOrigin, branch, release, commit, dependencyOptions)
		}
		if errInstallDep != nil {
			return nil, fmt.Errorf("failed to install dependency %s: %w", depOrigin, errInstallDep)
		}

		refs = append(refs, types.Dependency{
			Origin:  depOrigin,
			Branch:  branch,
			Release: release,
			Commit:  commit,
			Mode:    depManifest.Mode,
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
			Mode:    ref.Mode,
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
	if len(app.ParsedDesktopEntries) > 0 {
		entries, entriesErr := c.fvsMergedEntries(app.ParsedLayers)
		if entriesErr != nil {
			return entriesErr
		}
		for _, entry := range app.ParsedDesktopEntries {
			err = c.exportFVSDesktopEntry(entries, app, entry)
			if err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	if err = removeLegacyDesktopExports(app); err != nil {
		return err
	}

	for _, binary := range app.ParsedBinaries {
		err = c.exportBinary(app, binary)
		if err != nil {
			return
		}
	}
	return
}

func (c *Cpak) exportFVSDesktopEntry(entries map[string]fvsViewEntry, app types.Application, desktopEntry string) error {
	entryName := strings.TrimPrefix(path.Clean(desktopEntry), "/")
	if _, ok := entries[entryName]; !ok {
		base := path.Base(entryName)
		names := make([]string, 0, len(entries))
		for name := range entries {
			if path.Base(name) == base {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return fmt.Errorf("desktop entry %s not found in application layers", base)
		}
		entryName = names[0]
	}
	data, err := fvsViewFileData(c.Ctx, entries, entryName)
	if err != nil {
		return err
	}
	return c.writeDesktopExport(entries, app, desktopEntry, string(data))
}

func (c *Cpak) writeDesktopExport(entries map[string]fvsViewEntry, app types.Application, desktopEntry, content string) error {
	entryBase := filepath.Base(desktopEntry)
	desktopDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
	if err := os.MkdirAll(desktopDir, 0o755); err != nil {
		return err
	}
	desktopDest := desktopEntryExportPath(app, entryBase)

	iconName := desktopEntryValue([]byte(content), "Icon")
	if iconName != "" {
		if iconEntry := findFVSIcon(entries, iconName); iconEntry != "" {
			iconData, err := fvsViewFileData(c.Ctx, entries, iconEntry)
			if err != nil {
				return err
			}
			extension := path.Ext(iconEntry)
			iconDest := filepath.Join(os.Getenv("HOME"), ".local", "share", "icons", applicationExportID(app)+extension)
			if err := os.MkdirAll(filepath.Dir(iconDest), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(iconDest, iconData, 0o644); err != nil {
				return err
			}
			logger.Printf("Exported icon to %s", iconDest)
			iconName = iconDest
		} else {
			logger.Printf("Warning: icon %s not found for app %s", iconName, app.Name)
		}
	}

	lines := strings.Split(content, "\n")
	launcher, err := desktopLauncherPath()
	if err != nil {
		return err
	}
	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "Exec="):
			lines[index] = rewriteDesktopExec(launcher, app.Origin, strings.TrimPrefix(line, "Exec="))
		case strings.HasPrefix(line, "TryExec="):
			lines[index] = "TryExec=" + launcher
		case strings.HasPrefix(line, "Icon=") && iconName != "":
			lines[index] = "Icon=" + iconName
		}
	}
	newContent := strings.Join(lines, "\n")
	if err := os.WriteFile(desktopDest, []byte(newContent), 0o755); err != nil {
		return err
	}
	return exportDesktopAlias(app, entryBase, newContent)
}

func findFVSIcon(entries map[string]fvsViewEntry, iconName string) string {
	if path.IsAbs(iconName) {
		name := strings.TrimPrefix(path.Clean(iconName), "/")
		if _, ok := entries[name]; ok {
			return name
		}
		return ""
	}
	iconPath := ""
	iconScore := -1
	for name := range entries {
		base := path.Base(name)
		extension := strings.ToLower(path.Ext(base))
		if extension != ".png" && extension != ".svg" && extension != ".xpm" {
			continue
		}
		if base != iconName && strings.TrimSuffix(base, extension) != iconName {
			continue
		}
		score := 0
		if extension == ".svg" {
			score = 1000000
		}
		resolution := path.Base(path.Dir(path.Dir(name)))
		var width, height int
		if _, err := fmt.Sscanf(resolution, "%dx%d", &width, &height); err == nil {
			score += min(width, height)
		}
		if score > iconScore || score == iconScore && name < iconPath {
			iconPath = name
			iconScore = score
		}
	}
	return iconPath
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

	desktopDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		return err
	}
	desktopDest := desktopEntryExportPath(app, entryBase)

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
	var absIconPath string
	if iconName != "" {
		absIconPath = findIcon(rootFs, iconName)

		if absIconPath == "" && filepath.IsAbs(iconName) {
			if _, err := os.Stat(iconName); err == nil {
				absIconPath = iconName
			}
		}
	}

	if absIconPath != "" {
		ext := filepath.Ext(absIconPath)
		iconDest := filepath.Join(os.Getenv("HOME"), ".local", "share", "icons", applicationExportID(app)+ext)
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
	launcher, err := desktopLauncherPath()
	if err != nil {
		return err
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "Exec=") {
			lines[i] = rewriteDesktopExec(launcher, app.Origin, strings.TrimPrefix(line, "Exec="))
		}
		if strings.HasPrefix(line, "TryExec=") {
			lines[i] = "TryExec=" + launcher
		}
		if strings.HasPrefix(line, "Icon=") && iconName != "" {
			lines[i] = "Icon=" + iconName
		}
	}
	newContent := strings.Join(lines, "\n")
	if err := os.WriteFile(desktopDest, []byte(newContent), 0755); err != nil {
		return err
	}
	return exportDesktopAlias(app, entryBase, newContent)
}

func exportDesktopAlias(app types.Application, name, content string) error {
	path := originalDesktopEntryExportPath(name)
	if existing, err := os.ReadFile(path); err == nil {
		if desktopEntryValue(existing, "X-cpak-Origin") != app.Origin {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if desktopEntryExistsInSystemData(name) {
		return nil
	}

	content = setDesktopEntryValue(content, "NoDisplay", "true")
	content = setDesktopEntryValue(content, "X-cpak-Origin", app.Origin)
	content = setDesktopEntryValue(content, "X-cpak-ID", app.CpakId)
	return os.WriteFile(path, []byte(content), 0644)
}

func originalDesktopEntryExportPath(name string) string {
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", filepath.Base(name))
}

func desktopEntryExistsInSystemData(name string) bool {
	directories := os.Getenv("XDG_DATA_DIRS")
	if directories == "" {
		directories = "/usr/local/share:/usr/share"
	}
	for _, directory := range strings.Split(directories, ":") {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, "applications", filepath.Base(name))); err == nil {
			return true
		}
	}
	return false
}

func setDesktopEntryValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	inDesktopEntry := false
	insertAt := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if inDesktopEntry {
				insertAt = i
				break
			}
			inDesktopEntry = line == "[Desktop Entry]"
			continue
		}
		if !inDesktopEntry {
			continue
		}
		insertAt = i + 1
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
			return strings.Join(lines, "\n")
		}
	}
	if insertAt < 0 {
		return content
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = key + "=" + value
	return strings.Join(lines, "\n")
}

func removeDesktopAlias(app types.Application, name string) error {
	path := originalDesktopEntryExportPath(name)
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if desktopEntryValue(content, "X-cpak-ID") != app.CpakId {
		return nil
	}
	return os.Remove(path)
}

func rewriteDesktopExec(launcher, origin, command string) string {
	command = strings.TrimSpace(command)
	end := len(command)
	quoted := false
	escaped := false
	for i := 0; i < len(command); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch command[i] {
		case '\\':
			escaped = true
		case '"':
			quoted = !quoted
		case ' ', '\t':
			if !quoted {
				end = i
				i = len(command)
			}
		}
	}

	binary := command[:end]
	if strings.HasPrefix(binary, "\"") {
		binary = "\"@" + strings.TrimPrefix(binary, "\"")
	} else {
		binary = "@" + binary
	}
	rewritten := "Exec=" + desktopExecArgument(launcher) + " run " + origin + " " + binary
	if arguments := strings.TrimSpace(command[end:]); arguments != "" {
		rewritten += " -- " + arguments
	}
	return rewritten
}

func desktopEntryExportPath(app types.Application, desktopEntry string) string {
	name := applicationExportID(app) + "-" + filepath.Base(desktopEntry)
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", name)
}

func applicationExportID(app types.Application) string {
	hash := sha256.Sum256([]byte(app.CpakId))
	return fmt.Sprintf("cpak-%x", hash)
}

func removeLegacyDesktopExports(app types.Application) error {
	home := os.Getenv("HOME")
	path := filepath.Join(home, ".local", "share", "applications", app.CpakId)
	if err := os.RemoveAll(path); err != nil {
		return err
	}

	iconsDir := filepath.Join(home, ".local", "share", "icons")
	if _, err := os.Stat(iconsDir); os.IsNotExist(err) {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(iconsDir, app.CpakId+".*"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
	installedVersions, err := store.GetApplicationsByOrigin(origin, "", "", "", "")
	if err != nil {
		_ = store.Close()
		return err
	}
	removedSessions, _ := sessionsRemovedByVersionSelection(installedVersions, branch, commit, release)
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
	users, err = c.dependencyUsers(appToRemove.CpakId)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		origins := make([]string, 0, len(users))
		for _, user := range users {
			origins = append(origins, user.Origin)
		}
		return fmt.Errorf("application %s is required by %s", origin, strings.Join(origins, ", "))
	}

	// Stop all containers associated with the application
	err = c.Stop(appToRemove.Origin, appToRemove.Version, appToRemove.Branch, appToRemove.Commit, appToRemove.Release)
	if err != nil {
		return fmt.Errorf("failed to stop containers for %s: %w", appToRemove.Name, err)
	}
	if len(removedSessions) > 0 {
		if err = disableRegisteredSessions(systemauthority.DefaultRegistry(), origin, removedSessions, systemauthority.Remove); err != nil {
			return fmt.Errorf("disable login sessions for %s: %w", appToRemove.Name, err)
		}
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

	return c.removeApplicationLayers(appToRemove)
}

func sessionsRemovedByVersionSelection(apps []types.Application, branch, commit, release string) ([]types.Session, int) {
	selected := []types.Session{}
	remainingSessions := map[string]bool{}
	remaining := 0
	for _, app := range apps {
		matches := branch != "" && app.Branch == branch ||
			commit != "" && app.Commit == commit ||
			release != "" && app.Release == release
		if !matches {
			remaining++
			for _, session := range app.ParsedSessions {
				remainingSessions[session.ID] = true
			}
			continue
		}
		selected = append(selected, app.ParsedSessions...)
	}
	sessions := make([]types.Session, 0, len(selected))
	for _, session := range selected {
		if !remainingSessions[session.ID] {
			sessions = append(sessions, session)
		}
	}
	return sessions, remaining
}

func (c *Cpak) dependencyUsers(cpakID string) ([]types.Application, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	apps, err := store.GetApplications()
	if err != nil {
		return nil, err
	}
	users := make([]types.Application, 0)
	for _, app := range apps {
		for _, dependency := range app.ParsedDependencies {
			if dependency.Id == cpakID {
				users = append(users, app)
				break
			}
		}
	}
	return users, nil
}

func (c *Cpak) removeExports(app types.Application) error {
	home := os.Getenv("HOME")

	if err := removeLegacyDesktopExports(app); err != nil {
		logger.Printf("Warning: could not remove legacy desktop entries for %s: %v", app.Name, err)
	}
	for _, entry := range app.ParsedDesktopEntries {
		path := desktopEntryExportPath(app, entry)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logger.Printf("Warning: could not remove desktop entry %s: %v", path, err)
		}
		if err := removeDesktopAlias(app, entry); err != nil {
			logger.Printf("Warning: could not remove desktop alias %s: %v", entry, err)
		}
	}

	iconsDir := filepath.Join(home, ".local", "share", "icons")
	entries, err := os.ReadDir(iconsDir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, applicationExportID(app)+".") {
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
		if err := removeLegacyDesktopExports(old); err != nil {
			return err
		}
		for _, entry := range old.ParsedDesktopEntries {
			if err := os.Remove(desktopEntryExportPath(old, entry)); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := removeDesktopAlias(old, entry); err != nil {
				return err
			}
		}

		iconsDir := filepath.Join(home, ".local", "share", "icons")
		entries, err := os.ReadDir(iconsDir)
		if err == nil {
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), applicationExportID(old)+".") {
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

		if err := removeLegacyDesktopExports(old); err != nil {
			return err
		}
		for _, entry := range old.ParsedDesktopEntries {
			name := filepath.Base(entry)
			if keptEntries[name] {
				continue
			}
			if err := os.Remove(desktopEntryExportPath(old, entry)); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := removeDesktopAlias(old, entry); err != nil {
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
