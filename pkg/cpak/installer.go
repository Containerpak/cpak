/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
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

	// PulledIn marks an installation the user never named, which is every
	// dependency an installation drags in behind the package that was asked
	// for. It is recorded on the application, since the answer is needed long
	// after the install is over.
	PulledIn bool

	// PulledInBy is the origin of the package that is dragging this one in,
	// which is the one installation with a say in how it starts on its own.
	// It travels with PulledIn and means nothing without it.
	PulledInBy string

	// ResolvedDependencies are the dependency manifests the caller already
	// fetched, so that a client which had to resolve the graph to describe it
	// does not make cpak fetch every one of them a second time. A lock still
	// wins over them: it is the stronger statement about the same graph.
	ResolvedDependencies []ResolvedDependency

	dependencyState *dependencyInstallState
	dependencyDepth int
}

const (
	maxDependencyDepth = 32
	maxDependencyCount = 256
)

type dependencyInstallState struct {
	active map[string]bool
	total  int
}

// InstallWithOptions installs a remote package with explicit options.
func (c *Cpak) InstallWithOptions(origin, branch, release, commit string, options InstallOptions) (err error) {
	origin, err = normalizeRepositoryOrigin(origin)
	if err != nil {
		return err
	}
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

	// This is where an installation is configured, and it is the only place
	// the migration belongs: validation is what the lock and the publisher's
	// signature both hash after, so migrating there would change the manifest
	// every digest is taken over.
	override, err := installedOverride(manifest)
	if err != nil {
		return
	}
	if err = c.refuseCpakStateGrants(override, manifest.Sessions); err != nil {
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
		// Naming an origin that is already here as somebody else's dependency
		// is the user asking for it in their own right, and it is the only way
		// back out of the narrowing: the record stops being pulled in, it gets
		// the launchers it was never given, and its next launch answers to
		// nobody but its own policy. Nothing here widens the other direction,
		// since a dependency install of a package the user named leaves the
		// record exactly as it found it.
		if existingApp.PulledIn && !options.PulledIn {
			existingApp.PulledIn = false
			existingApp.PulledInBy = ""
			if err = c.storeApplication(existingApp); err != nil {
				return
			}
			if options.CreateExports {
				if err = c.createExports(existingApp); err != nil {
					return
				}
			}
			logger.Printf("%s was already here as a dependency of another package and is now installed in its own right", manifest.Name)
			return
		}
		logger.Printf("%s is already installed from %s; run an audit if it is not working as expected", manifest.Name, origin)
		return
	}
	if options.dependencyState == nil {
		options.dependencyState = &dependencyInstallState{active: make(map[string]bool)}
	}
	if options.dependencyDepth >= maxDependencyDepth {
		return fmt.Errorf("dependency graph exceeds the maximum depth of %d", maxDependencyDepth)
	}
	dependencyKey := lockedPackageKey(origin, branch, release, commit)
	if options.dependencyState.active[dependencyKey] {
		return fmt.Errorf("dependency cycle at %s", origin)
	}
	if options.dependencyState.total >= maxDependencyCount {
		return fmt.Errorf("dependency graph exceeds the maximum of %d packages", maxDependencyCount)
	}
	options.dependencyState.active[dependencyKey] = true
	options.dependencyState.total++
	defer delete(options.dependencyState.active, dependencyKey)

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
	pulled := layers
	layers, err = c.BuildRuntimeLayers(layers, manifest.RuntimeSources)
	if err != nil {
		return
	}
	layers, err = c.BuildLocaleLayer(layers, manifest.Image, config, manifest.Override)
	if err != nil {
		return
	}
	if err = c.bindBuiltLayers(pulled, layers); err != nil {
		return
	}

	manifestDigest, err := ManifestIdentityDigest(manifest)
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
		ParsedAddonProvider:  manifest.AddonProvider,
		IdleTime:             manifest.IdleTime,
		ParsedLayers:         layers,
		RuntimeSources:       manifest.RuntimeSources,
		Config:               config,
		Image:                image,
		ImageDigest:          imageDigest,
		ManifestDigest:       manifestDigest,
		ParsedOverride:       override,
		PulledIn:             options.PulledIn,
		PulledInBy:           options.PulledInBy,
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

	// The installation stands: what is left is to record what it is, so that a
	// launch of it can be recognised. It reports and it never fails an install.
	//
	// The manifest goes with it because this is the only moment it exists. What
	// a publisher signs is the manifest as cpak applied it beside the image it
	// resolved to, and nothing the store keeps can name that pair afterwards,
	// so an enrolment that did not get it here can never ask the registry who
	// published this.
	c.EnrolPublishedApplication(app, PublishedPackage{
		Manifest: manifest,
		Lock:     signedLock(origin, options.ManifestLock),
	})

	return nil
}

// refuseCpakStateGrants keeps an installation out of cpak's own tree. It runs
// here, where an installation is configured, and not inside the shared
// validator: that one also runs at every launch over the grants an application
// already has, so the same rule there would stop an application installed
// before the rule existed from starting at all. A stale grant is hidden by the
// mask instead, which is what the mask is for.
func (c *Cpak) refuseCpakStateGrants(override types.Override, sessions []types.Session) error {
	directories := c.cpakStateDirectories()
	if err := types.RefuseCpakStateGrants(override.Filesystem, directories); err != nil {
		return err
	}
	for _, session := range sessions {
		if err := types.RefuseCpakStateGrants(session.Override.Filesystem, directories); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
	}
	return nil
}

// ManifestIdentityDigest names the manifest an installation was made from, as
// the publisher wrote it: validation fills the defaults in and changes nothing
// else, and what an installation applies on top of it is decided afterwards.
// The lock and the publisher's signature both hash the manifest at this same
// point, so all three name one thing. The decoded manifest is hashed rather
// than the bytes that were fetched, because an installation resolved from a
// lock never sees those bytes and both paths must name the same manifest.
func ManifestIdentityDigest(manifest *types.CpakManifest) (string, error) {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode the manifest of %s: %w", manifest.Name, err)
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.manifest.v%d\n", integrity.ABIVersion)
	hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// ResolvedDependency is a package an installation pulls in on its own: the
// origin it resolves to, the selector it is taken at, and the manifest its own
// publisher wrote, which is the one that asks for the permissions it will run
// with.
type ResolvedDependency struct {
	Origin   string
	Branch   string
	Release  string
	Commit   string
	Mode     string
	Manifest *types.CpakManifest
}

// ResolveDependencies answers with every package an installation of the given
// manifest would install beside it, transitively and in the order they are
// installed.
//
// A dependency is installed as a package of its own, with the permissions its
// publisher asked for and not the ones the parent asked for, so a client that
// puts an installation to the user cannot describe it without these. The
// manifests are fetched here rather than at install time so that the answer is
// in hand before anything is installed, and the answer can be handed back to
// the installer through InstallOptions so that nothing is fetched twice.
func (c *Cpak) ResolveDependencies(origin string, manifest *types.CpakManifest) ([]ResolvedDependency, error) {
	return c.resolveDependencies(origin, manifest, c.FetchManifest, map[string]bool{})
}

func (c *Cpak) resolveDependencies(origin string, manifest *types.CpakManifest, fetchManifest func(string, string, string, string) (*types.CpakManifest, error), seen map[string]bool) ([]ResolvedDependency, error) {
	state := dependencyResolutionState{
		active: map[string]bool{origin: true},
		seen:   seen,
	}
	return c.resolveDependencyGraph(origin, manifest, fetchManifest, &state, 0)
}

type dependencyResolutionState struct {
	active map[string]bool
	seen   map[string]bool
	total  int
}

func (c *Cpak) resolveDependencyGraph(origin string, manifest *types.CpakManifest, fetchManifest func(string, string, string, string) (*types.CpakManifest, error), state *dependencyResolutionState, depth int) ([]ResolvedDependency, error) {
	if depth >= maxDependencyDepth {
		return nil, fmt.Errorf("dependency graph exceeds the maximum depth of %d", maxDependencyDepth)
	}
	resolved := []ResolvedDependency{}
	for _, declared := range manifest.Dependencies {
		dependencyOrigin, err := resolveDependencyOrigin(origin, declared.Origin)
		if err != nil {
			return nil, err
		}
		branch, release, commit := dependencySelectors(declared)

		if state.active[dependencyOrigin] {
			return nil, fmt.Errorf("dependency cycle at %s", dependencyOrigin)
		}
		key := lockedPackageKey(dependencyOrigin, branch, release, commit)
		if state.seen[key] {
			continue
		}
		if state.total >= maxDependencyCount {
			return nil, fmt.Errorf("dependency graph exceeds the maximum of %d packages", maxDependencyCount)
		}
		state.seen[key] = true
		state.total++
		state.active[dependencyOrigin] = true

		dependencyManifest, err := fetchManifest(dependencyOrigin, branch, release, commit)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve dependency %s: %w", dependencyOrigin, err)
		}
		if dependencyManifest == nil {
			return nil, fmt.Errorf("failed to resolve dependency %s: no manifest returned", dependencyOrigin)
		}
		// The whole point of resolving here is that what comes back is put to
		// the user, and a manifest nobody has checked is publisher text on its
		// way to a terminal. It is held to the same rules every other fetched
		// manifest is held to, and it is held to them before it is described
		// rather than when the install reaches it.
		if err = c.ValidateManifest(dependencyManifest); err != nil {
			return nil, fmt.Errorf("failed to resolve dependency %s: %w", dependencyOrigin, err)
		}
		resolved = append(resolved, ResolvedDependency{
			Origin:   dependencyOrigin,
			Branch:   branch,
			Release:  release,
			Commit:   commit,
			Mode:     declared.Mode,
			Manifest: dependencyManifest,
		})

		nested, err := c.resolveDependencyGraph(dependencyOrigin, dependencyManifest, fetchManifest, state, depth+1)
		delete(state.active, dependencyOrigin)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, nested...)
	}
	return resolved, nil
}

// resolvedDependencyManifest answers with the manifest the caller already
// fetched for the given package, if it fetched one.
func resolvedDependencyManifest(resolved []ResolvedDependency, origin, branch, release, commit string) (*types.CpakManifest, bool) {
	key := lockedPackageKey(origin, branch, release, commit)
	for _, dependency := range resolved {
		if lockedPackageKey(dependency.Origin, dependency.Branch, dependency.Release, dependency.Commit) == key {
			return dependency.Manifest, true
		}
	}
	return nil, false
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
		// The user named the package that declares this one, never this one,
		// and the record says so from here on, together with which package it
		// came here behind.
		dependencyOptions.PulledIn = true
		dependencyOptions.PulledInBy = origin
		dependencyOptions.dependencyDepth = options.dependencyDepth + 1
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
		} else if resolved, ok := resolvedDependencyManifest(options.ResolvedDependencies, depOrigin, branch, release, commit); ok {
			errInstallDep = c.InstallCpakWithOptions(depOrigin, resolved, branch, commit, release, dependencyOptions)
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
//
// A package the user never named gets none. A launcher in the menu and a
// binary on the path are an invitation to start a package nobody was shown,
// under the permissions its own publisher asked for, and the user agreed to
// the package that pulled it in rather than to this one; that package still
// reaches it through a nested run, which holds it to the intersection.
//
// The answer is given here rather than at the install, because the install is
// not the only path that exports: an update, a rollback and an aborted
// transaction all rebuild the exports of an application from what the store
// holds, and an origin that flipped between exported and not depending on
// which command touched it last would be worse than either answer.
func (c *Cpak) createExports(app types.Application) (err error) {
	if app.PulledIn {
		return nil
	}
	if len(app.ParsedDesktopEntries) > 0 {
		entries, entriesErr := c.fvsMergedEntries(app.ParsedLayers)
		if entriesErr != nil {
			return entriesErr
		}
		for _, entry := range app.ParsedDesktopEntries {
			if _, nameErr := desktopEntryExportName(entry); nameErr != nil {
				// The manifest check refuses this at install time, but an
				// application installed before it keeps the name in the store
				// and every update and repair re-exports from there. Leaving
				// the entry unwritten is the answer; refusing to update the
				// application the user already has is not.
				logger.Printf("Warning: %v, skipping its export", nameErr)
				continue
			}
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
	if refreshErr := refreshDesktopDatabase(); refreshErr != nil {
		logger.Printf("Warning: could not refresh the desktop database: %v", refreshErr)
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
	data, err := fvsViewFileData(c.Ctx, entries, entryName, desktopEntrySizeLimit)
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
			iconData, err := fvsViewFileData(c.Ctx, entries, iconEntry, iconSizeLimit)
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
		key, value, ok := desktopEntryKey(line)
		if !ok {
			continue
		}
		switch key {
		case "Exec":
			lines[index] = rewriteDesktopExec(launcher, app.Origin, value)
		case "TryExec":
			lines[index] = "TryExec=" + launcher
		case "Icon":
			if iconName != "" {
				lines[index] = "Icon=" + iconName
			}
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

func exportDesktopAlias(app types.Application, name, content string) error {
	name, nameErr := desktopEntryExportName(name)
	if nameErr != nil {
		logger.Printf("Warning: %v, skipping its alias", nameErr)
		return nil
	}
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
	// Every guard around this file, the one above and the one at removal, reads
	// the marker back out of it, so a serialisation that took none is a file
	// cpak owns and can never name again. setDesktopEntryValue answers the
	// content unchanged when there is no [Desktop Entry] group to put a key in,
	// which is exactly the file that is not a launcher.
	if desktopEntryValue([]byte(content), "X-cpak-ID") != app.CpakId {
		logger.Printf("Warning: desktop entry %q carries no [Desktop Entry] group to mark, skipping its alias", name)
		return nil
	}
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

// setDesktopEntryValue writes a key into the [Desktop Entry] group of a
// desktop file, adding it when it is not already there.
//
// It reads the file the way desktopEntryValue does, which is the reader every
// guard around the export uses: the group header is recognised after trimming,
// and a file written on Windows or opened by an editor that leaves a byte
// order mark is still a launcher. Recognising less here than the readers do
// meant the marker silently never landed, and an alias without a marker is one
// the uninstall walks past.
func setDesktopEntryValue(content, key, value string) string {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	inDesktopEntry := false
	insertAt := -1
	for i, line := range lines {
		line = strings.TrimSpace(line)
		// A group header is a line that opens one, which is what
		// desktopEntryValue reads and therefore what a group header has to be
		// here. Requiring the closing bracket as well ended the group for the
		// reader and not for the writer, so on a file carrying a malformed
		// header the marker went into a group the reader never looks in.
		if strings.HasPrefix(line, "[") {
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
	rewritten := "Exec=" + desktopExecArgument(launcher) + " run --desktop-launch " + origin
	arguments := strings.TrimSpace(command[end:])
	tokens := splitDesktopArguments(arguments)
	// Which arguments are files the user chose is decided here, by counting,
	// and travels in a flag of cpak's own. It used to be delimited by markers
	// inside the publisher's own text, where the publisher could write one.
	span, selects, spanErr := countDesktopFileSpan(tokens)
	if spanErr != nil {
		// An entry cpak cannot describe exports with no file grant at all
		// rather than with one it is guessing at.
		selects = false
	}
	if selects {
		rewritten += " " + desktopFileSpanFlag + " " + span.String()
	}
	rewritten += " " + binary
	if arguments != "" {
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

// shellLiteral renders a value as a single word for /bin/sh.
func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (c *Cpak) exportBinary(app types.Application, binary string) error {
	origin, err := normalizeRepositoryOrigin(app.Origin)
	if err != nil {
		return err
	}
	relative := filepath.Join(filepath.FromSlash(origin), filepath.Base(binary))
	destinationPath, err := containedPath(c.Options.ExportsPath, relative)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(destinationPath), 0755)
	if err != nil {
		return err
	}

	// A bare name here is resolved through PATH, which any writer of the home
	// can rearrange. The launcher is named outright.
	launcher, err := getCpakBinary()
	if err != nil {
		return err
	}
	// The words the manifest chose are quoted, not formatted in: a wrapper is
	// a shell script, and what an application names is an argument to the
	// launcher, never a piece of the script. The launcher itself is named
	// outright, as above.
	scriptContent := "#!/bin/sh\nexec " + launcher + " run " + shellLiteral(app.Origin) + " " + shellLiteral("@"+binary) + " -- \"$@\"\n"
	err = os.WriteFile(destinationPath, []byte(scriptContent), 0755)
	if err != nil {
		return err
	}
	return nil
}

// Remove removes a package from the local store, including all the containers,
// exports and unshared layers associated with it. Persistent application data
// is retained.
func (c *Cpak) Remove(origin string, branch string, commit string, release string) error {
	return c.remove(origin, branch, commit, release, false)
}

// Purge removes a package and its persistent application data.
func (c *Cpak) Purge(origin string, branch string, commit string, release string) error {
	return c.remove(origin, branch, commit, release, true)
}

func (c *Cpak) remove(origin string, branch string, commit string, release string, purge bool) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	installedVersions, err := store.GetApplicationsByOrigin(origin, "", "", "", "")
	if err != nil {
		_ = store.Close()
		return err
	}
	if branch == "" && commit == "" && release == "" {
		if len(installedVersions) == 0 {
			_ = store.Close()
			return fmt.Errorf("application %s not found for removal", origin)
		}
		if len(installedVersions) > 1 {
			_ = store.Close()
			return fmt.Errorf("multiple installations of %s found; specify a branch, commit or release", origin)
		}
		branch = installedVersions[0].Branch
		commit = installedVersions[0].Commit
		release = installedVersions[0].Release
		if branch == "" && commit == "" && release == "" {
			_ = store.Close()
			return fmt.Errorf("installed application %s has no source selector", origin)
		}
	}

	appToRemove, err := store.GetApplicationByOrigin(origin, "", branch, commit, release)
	if err != nil || appToRemove.CpakId == "" {
		_ = store.Close()
		return fmt.Errorf("application %s not found for specified criteria: %w", origin, err)
	}
	removedSessions, remainingVersions := sessionsRemovedByVersionSelection(installedVersions, branch, commit, release)
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
	if err = c.removeApplicationLayers(appToRemove); err != nil {
		return err
	}
	if purge {
		if err = c.purgeApplicationData(appToRemove, remainingVersions == 0); err != nil {
			return fmt.Errorf("purge application data for %s: %w", appToRemove.Name, err)
		}
	}

	// The layers are gone, so nothing the anchor names is still on disk.
	c.forgetEnrolment(appToRemove)

	return nil
}

func (c *Cpak) purgeApplicationData(app types.Application, clearOriginState bool) error {
	if path, err := c.applicationDataPath(app.CpakId); err == nil {
		if err = os.RemoveAll(path); err != nil {
			return err
		}
	}
	identity, err := c.applicationIdentityPath(app.CpakId)
	if err != nil {
		return err
	}
	if err = os.Remove(identity); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !clearOriginState {
		return nil
	}
	grants := filegrant.Store{Directory: filepath.Join(c.Options.StorePath, "grants")}
	return grants.Clear(app.Origin)
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
	if refreshErr := refreshDesktopDatabase(); refreshErr != nil {
		logger.Printf("Warning: could not refresh the desktop database: %v", refreshErr)
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
	if refreshErr := refreshDesktopDatabase(); refreshErr != nil {
		logger.Printf("Warning: could not refresh the desktop database: %v", refreshErr)
	}

	return nil
}
