package cpak

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Install installs a package from a given origin. The origin must be a git
// repository with a valid cpak manifest file in the root directory.
// The branch, release and commit parameters are used to select the version of
// the package to install. Note that those parameters are mutually exclusive,
// the installation will fail if more than one of them is specified.
func (c *Cpak) Install(origin, branch, release, commit string) (err error) {
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

	return c.InstallCpak(origin, manifest)
}

// InstallCpak installs a package from a given manifest file.
//
// Note: this function can be used to install packages from a local manifest
// but this behaviour is not fully supported yet.
func (c *Cpak) InstallCpak(origin string, manifest *types.CpakManifest) (err error) {
	err = c.ValidateManifest(manifest)
	if err != nil {
		return
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	var existingApp types.Application
	existingApp, err = store.GetApplicationByOrigin(origin, manifest.Version)
	if err != nil {
		return
	}

	if existingApp.Id != "" {
		return fmt.Errorf("application already installed, perform an Audit if this application is not working as expected")
	}

	imageId := base64.StdEncoding.EncodeToString([]byte(manifest.Name + ":" + manifest.Version))
	layers, config, err := c.Pull(manifest.Image, imageId)
	if err != nil {
		return
	}

	app := types.Application{
		Id:                 imageId,
		Name:               manifest.Name,
		Version:            manifest.Version,
		Origin:             origin,
		Timestamp:          time.Now(),
		Binaries:           manifest.Binaries,
		DesktopEntries:     manifest.DesktopEntries,
		FutureDependencies: manifest.FutureDependencies,
		Layers:             layers,
		Config:             config,
	}

	err = store.NewApplication(app)
	if err != nil {
		return
	}

	err = store.db.Close()
	if err != nil {
		return
	}

	// err = c.CreateExports(app)
	// if err != nil {
	// 	return
	// }

	return nil
}

// Remove removes a package from the local store, including all the containers
// and exports associated with it. It also removes the application and
// container files from the cpak data directory.
func (c *Cpak) Remove(name string) (err error) {
	panic("not implemented")
}

// CreateExports creates the exports for a given application.
func (c *Cpak) CreateExports(app types.Application) (err error) {
	panic("not implemented")
	// TODO: before implementing this, we have to resolve dependencies
}
