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
	destinationPath := filepath.Join(
		os.Getenv("HOME"),
		".local",
		"share",
		"applications",
		filepath.Base(desktopEntry),
	)

	originalPath := filepath.Join(rootFs, desktopEntry)
	desktopEntryContent, err := os.ReadFile(originalPath)
	if err != nil {
		return err
	}

	iconPath := ""
	lines := strings.Split(string(desktopEntryContent), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Icon=") {
			iconPath = strings.TrimPrefix(line, "Icon=")
			break
		}
	}

	if iconPath != "" && !filepath.IsAbs(iconPath) {
		// Try to find the icon in /usr/share/icons
		commonIconDirs := []string{"scalable", "512x512", "256x256", "128x128", "64x64", "48x48", "32x32"}
		for _, commonIconDir := range commonIconDirs {
			inRootFsIconPath := filepath.Join(rootFs, "usr", "share", "icons", "hicolor", commonIconDir, "apps", iconPath)
			if _, statErr := os.Stat(inRootFsIconPath); statErr == nil {
				iconPath = inRootFsIconPath
				break
			}
			if _, statErr := os.Stat(inRootFsIconPath + ".svg"); statErr == nil {
				iconPath = inRootFsIconPath + ".svg"
				break
			}
			if _, statErr := os.Stat(inRootFsIconPath + ".png"); statErr == nil {
				iconPath = inRootFsIconPath + ".png"
				break
			}
		}

		// If not found in /usr/share/icons, try /usr/share/pixmaps
		if !filepath.IsAbs(iconPath) {
			inRootFsPixmapPath := filepath.Join(rootFs, "usr", "share", "pixmaps", iconPath)
			if _, statErr := os.Stat(inRootFsPixmapPath); statErr == nil {
				iconPath = inRootFsPixmapPath
			} else if _, statErr := os.Stat(inRootFsPixmapPath + ".png"); statErr == nil {
				iconPath = inRootFsPixmapPath + ".png"
			} else if _, statErr := os.Stat(inRootFsPixmapPath + ".svg"); statErr == nil {
				iconPath = inRootFsPixmapPath + ".svg"
			}
		}

		if !filepath.IsAbs(iconPath) {
			var iconLine string
			for _, line := range lines {
				if strings.HasPrefix(line, "Icon=") {
					iconLine = strings.TrimPrefix(line, "Icon=")
					break
				}
			}
			fmt.Printf("Warning: could not resolve absolute path for icon '%s' for app %s\n", iconLine, app.Name)
			// return nil // Non bloccare l'esportazione del .desktop se l'icona non si trova
		} else {
			destinationIconPath := filepath.Join(
				os.Getenv("HOME"),
				".local",
				"share",
				"icons",
				filepath.Base(iconPath),
			)
			err = os.MkdirAll(filepath.Dir(destinationIconPath), 0755)
			if err != nil {
				return err
			}
			err = tools.CopyFile(iconPath, destinationIconPath)
			if err != nil {
				return err
			}
		}
	}

	desktopEntryContentStr := strings.ReplaceAll(string(desktopEntryContent), "Exec=", "Exec=cpak run "+app.Origin+" @")
	if err := os.WriteFile(destinationPath, []byte(desktopEntryContentStr), 0755); err != nil {
		return err
	}
	return nil
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
	for _, entry := range app.ParsedDesktopEntries {
		destinationPath := filepath.Join(
			os.Getenv("HOME"),
			".local",
			"share",
			"applications",
			filepath.Base(entry),
		)
		if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: could not remove desktop entry %s: %v\n", destinationPath, err)
		}
	}

	for _, binary := range app.ParsedBinaries {
		destinationItems := []string{c.Options.ExportsPath}
		destinationItems = append(destinationItems, strings.Split(app.Origin, "/")...)
		destinationItems = append(destinationItems, filepath.Base(binary))
		destinationPath := filepath.Join(destinationItems...)
		if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: could not remove binary export %s: %v\n", destinationPath, err)
		}

		originDir := filepath.Dir(destinationPath)
		if entries, err := os.ReadDir(originDir); err == nil && len(entries) == 0 {
			os.Remove(originDir)
			repoDir := filepath.Dir(originDir)
			if entriesRepo, errRepo := os.ReadDir(repoDir); errRepo == nil && len(entriesRepo) == 0 {
				os.Remove(repoDir)
				hostDir := filepath.Dir(repoDir)
				if entriesHost, errHost := os.ReadDir(hostDir); errHost == nil && len(entriesHost) == 0 {
					os.Remove(hostDir)
				}
			}
		}
	}
	return nil
}
