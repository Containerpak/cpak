package cpak

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	manifest, err := c.FetchManifest(origin, branch, release, commit)
	if err != nil {
		return err
	}

	return c.InstallCpak(origin, manifest, branch, commit, release)
}

// InstallCpak installs a package from a given manifest file.
//
// Note: this function can be used to install packages from a local manifest
// but this behaviour is not fully supported yet.
func (c *Cpak) InstallCpak(origin string, manifest *types.CpakManifest, branch string, commit string, release string) (err error) {
	err = c.ValidateManifest(manifest)
	if err != nil {
		return
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

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

	existingApp, _ := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if existingApp.CpakId != "" {
		fmt.Println("application already installed, perform an Audit if this application is not working as expected")
		return
	}

	// first we resolve its dependencies
	var parsedManifestDependencies []types.Dependency
	for _, depManifest := range manifest.Dependencies {
		depOrigin := depManifest.Origin
		if !isURL(depOrigin) {
			fmt.Printf("dependency %s is not a valid cpak url, assuming it comes from the same origin\n", depOrigin)
			parentOrigin := origin[:strings.LastIndex(origin, "/")]
			depOrigin = parentOrigin + "/" + depOrigin
		}

		depBranch := "main"
		if depManifest.Branch != "" {
			depBranch = depManifest.Branch
		}

		errInstallDep := c.Install(depOrigin, depBranch, depManifest.Release, depManifest.Commit)
		if errInstallDep != nil {
			return fmt.Errorf("failed to install dependency %s: %w", depOrigin, errInstallDep)
		}

		installedDepApp, errGetDep := store.GetApplicationByOrigin(depOrigin, depBranch, "", depManifest.Commit, depManifest.Release)
		if errGetDep != nil || installedDepApp.CpakId == "" {
			return fmt.Errorf("failed to retrieve installed dependency %s after installation attempt: %w", depOrigin, errGetDep)
		}
		parsedManifestDependencies = append(parsedManifestDependencies, types.Dependency{
			Id:      installedDepApp.CpakId,
			Origin:  installedDepApp.Origin,
			Branch:  installedDepApp.Branch,
			Release: installedDepApp.Release,
			Commit:  installedDepApp.Commit,
		})
	}

	imageIdBase := manifest.Name + ":" + sourceType + ":" + version + ":" + origin
	cpakImageId := base64.StdEncoding.EncodeToString([]byte(imageIdBase))

	layers, config, err := c.Pull(manifest.Image, cpakImageId)
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
		ParsedLayers:         layers,
		Config:               config,
		ParsedOverride:       manifest.Override,
	}

	err = c.createExports(app)
	if err != nil {
		return
	}

	err = store.NewApplication(app)
	if err != nil {
		return
	}

	return nil
}

func isURL(s string) bool {
	return len(s) > 3 && (strings.HasPrefix(s, "http") || strings.Contains(s, "/"))
}

// createExports creates the exports for a given application.
func (c *Cpak) createExports(app types.Application) (err error) {
	for _, entry := range app.ParsedDesktopEntries {
		for _, layer := range app.ParsedLayers {
			layerDir := c.GetInStoreDir("layers", layer)
			err = c.exportDesktopEntry(layerDir, app, entry)
			if err == nil {
				break
			}
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
	for _, layer := range app.ParsedLayers {
		layerDir := c.GetInStoreDir("layers", layer)
		cand := filepath.Clean(layerDir + iconName)
		if _, err := os.Stat(cand); err == nil {
			absIconPath = cand
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
		fmt.Printf("Exported icon to %s\n", iconDest)
		iconName = iconDest
	} else {
		fmt.Printf("Warning: icon %s not found for app %s\n", iconName, app.Name)
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

func (c *Cpak) exportBinary(app types.Application, binary string) error {
	destinationItems := []string{c.Options.ExportsPath}
	destinationItems = append(destinationItems, strings.Split(app.Origin, "/")...)
	destinationItems = append(destinationItems, filepath.Base(binary))
	destinationPath := filepath.Join(destinationItems...)

	err := os.MkdirAll(filepath.Dir(destinationPath), 0755)
	if err != nil {
		return err
	}

	scriptContent := fmt.Sprintf("#!/bin/sh\ncpak run %s @%s \"$@\"\n", app.Origin, binary)
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
	defer store.Close()

	appToRemove, err := store.GetApplicationByOrigin(origin, "", branch, commit, release)
	if err != nil || appToRemove.CpakId == "" {
		return fmt.Errorf("application %s not found for specified criteria: %w", origin, err)
	}

	// Stop all containers associated with the application
	err = c.Stop(appToRemove.Origin, appToRemove.Version, appToRemove.Branch, appToRemove.Commit, appToRemove.Release)
	if err != nil {
		return fmt.Errorf("failed to stop containers for %s: %w", appToRemove.Name, err)
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
		return fmt.Errorf("failed to remove application from store: %w", err)
	}

	err = c.removeExports(appToRemove)
	if err != nil {
		fmt.Printf("Warning: failed to remove all exports for %s: %v\n", appToRemove.Name, err)
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
		fmt.Printf("Warning: could not remove desktop entries dir %s: %v\n", desktopDir, err)
	}

	iconsDir := filepath.Join(home, ".local", "share", "icons")
	entries, err := os.ReadDir(iconsDir)
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, app.CpakId+".") {
				path := filepath.Join(iconsDir, name)
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					fmt.Printf("Warning: could not remove icon %s: %v\n", path, err)
				}
			}
		}
	}

	for _, binary := range app.ParsedBinaries {
		dst := filepath.Join(append([]string{c.Options.ExportsPath}, strings.Split(app.Origin, "/")...)...)
		dst = filepath.Join(dst, filepath.Base(binary))
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: could not remove binary export %s: %v\n", dst, err)
		}

		dir := filepath.Dir(dst)
		for dir != c.Options.ExportsPath && dir != "/" {
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) > 0 {
				break
			}
			os.Remove(dir)
			dir = filepath.Dir(dir)
		}
	}

	return nil
}
