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
func (c *Cpak) InstallCpak(origin string, manifest *types.Manifest) (err error) {
	err = c.ValidateManifest(manifest)
	if err != nil {
		return
	}

	imageId := base64.StdEncoding.EncodeToString([]byte(manifest.Name + ":" + manifest.Version))
	layers, config, err := c.Pull(manifest.Image, imageId)
	if err != nil {
		return
	}

	store, err := NewStore(c.Options.StorePath)
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
	fmt.Println(app.Layers)

	err = store.NewApplication(app)
	if err != nil {
		return
	}

	err = store.db.Close()
	if err != nil {
		return
	}

	return nil
}
