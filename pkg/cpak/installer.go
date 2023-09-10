package cpak

import (
	"fmt"
	"strings"
	"time"

	ceTypes "github.com/linux-immutability-tools/containers-wrapper/pkg/types"
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

	imageItems := strings.Split(manifest.Image, ":")
	if len(imageItems) != 2 {
		return fmt.Errorf("invalid image format: %s", manifest.Image)
	}
	base := imageItems[0]
	tag := imageItems[1]
	_, err = c.Ce.PullImage(base, tag, "", false, false)
	if err != nil {
		return
	}

	images, err := c.Ce.Images(map[string][]string{})
	if err != nil {
		return
	}

	var image ceTypes.ImageInfo
	for _, img := range images {
		if img.Tag == tag {
			_img, err := c.Ce.InspectImage(img.Id)
			if err != nil {
				return err
			}
			image = _img
			break
		}
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	err = store.NewApplication(types.Application{
		Id:                 image.Id,
		Name:               manifest.Name,
		Version:            manifest.Version,
		Origin:             origin,
		Timestamp:          time.Now(),
		Binaries:           manifest.Binaries,
		DesktopEntries:     manifest.DesktopEntries,
		FutureDependencies: manifest.FutureDependencies,
	})
	if err != nil {
		return
	}

	err = store.db.Close()
	if err != nil {
		return
	}

	return nil
}
