package cpak

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Pull pulls a remote image and unpacks it into the storage folder.
//
// Note: cpak does not offer a standard containers storage, it uses a custom
// storage based on the image layers.
func (c *Cpak) Pull(image string, cpakImageId string) (layers []string, ociConfig string, err error) {
	err = tools.ValidateImageName(image)
	if err != nil {
		return
	}

	// getting the v1.Image of the remote image
	img, err := crane.Pull(image, crane.WithContext(c.Ctx))
	if err != nil {
		return
	}

	// saving the image to the cache (the actual download)
	tarCachePath := c.GetInCacheDir(cpakImageId + ".tar")
	err = crane.SaveLegacy(img, image, tarCachePath)
	if err != nil {
		return
	}

	// unpacking the image layers into the storage/images folder
	layers, ociConfig, err = c.unpackImageLayers(cpakImageId, img, tarCachePath)
	if err != nil {
		return
	}

	return
}

// unpackImageLayers unpacks the image layers into the storage/images folder
// and returns the list of layers and the image config.
//
// Note: the image config is returned as a string because it is not used
// during the unpacking process, it is used only when creating a new cpak
// container, to setup the environment as the developer intended. It will be
// stored as a field of the image struct in the store and marshalled back
// to JSON when needed.
func (c *Cpak) unpackImageLayers(digest string, image v1.Image, tarCachePath string) (layers []string, ociConfig string, err error) {
	// create temporary directory for the image in the cpak cache
	inCacheDir, err := c.GetInCacheDirMkdir(digest)
	if err != nil {
		return
	}

	// unpack image tarball into temporary directory
	err = tools.TarUnpack(tarCachePath, inCacheDir)
	if err != nil {
		return
	}

	// read and decode the JSON manifest
	manifestPath := filepath.Join(inCacheDir, "manifest.json")
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return
	}
	defer manifestFile.Close()

	var manifestData []types.OciManifest
	err = json.NewDecoder(manifestFile).Decode(&manifestData)
	if err != nil {
		return
	}

	manifest := manifestData[0]
	layers = manifest.Layers

	// unpack layers, each layer is a tarball so we have to unpack each one
	// into different directories inside the layers directory, we use the
	// following scheme: <layer-hash.ext>:<layer-files>
	for _, layer := range layers {
		layerPath := filepath.Join(inCacheDir, layer)
		var layerFile *os.File
		layerFile, err = os.Open(layerPath)
		if err != nil {
			return
		}
		defer layerFile.Close()

		err = os.MkdirAll(filepath.Join(c.Options.StoreLayersPath, layer), 0755)
		if err != nil {
			return
		}

		err = tools.TarUnpack(layerPath, filepath.Join(c.Options.StoreLayersPath, layer))
		if err != nil {
			return
		}
	}

	// get the image config
	ociConfigPath := c.GetInCacheDir(digest, manifest.Config)
	ociConfigBytes, err := os.ReadFile(ociConfigPath)
	if err != nil {
		return
	}

	ociConfig = string(ociConfigBytes)
	return
}
