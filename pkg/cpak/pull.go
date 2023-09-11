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
	tarCachePath := filepath.Join(c.Options.CachePath, cpakImageId+".tar")
	err = crane.SaveLegacy(img, image, tarCachePath)
	if err != nil {
		return
	}

	// unpacking the image layers into the storage/images folder
	layers, _, ociConfig, err = c.unpackImageLayers(cpakImageId, img, tarCachePath)
	if err != nil {
		return
	}

	return
}

func (c *Cpak) unpackImageLayers(digest string, image v1.Image, tarCachePath string) (layers []string, imagePath string, ociConfig string, err error) {
	// create temporary directory for the image in the cpak cache
	inCacheDir := filepath.Join(c.Options.CachePath, digest)
	err = os.MkdirAll(inCacheDir, 0755)
	if err != nil {
		return
	}

	// unpack image tarball into temporary directory
	err = tools.TarUnpack(tarCachePath, inCacheDir)
	if err != nil {
		return
	}

	// create image directory in cpak storage
	imagePath = filepath.Join(c.Options.StorePath, "images", digest)
	err = os.MkdirAll(imagePath, 0755)
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
	// into different directories inside the image directory, we use the
	// following scheme: <image-name>:<layer-hash.ext>:<layer-files>
	for _, layer := range layers {
		layerPath := filepath.Join(inCacheDir, layer)
		var layerFile *os.File
		layerFile, err = os.Open(layerPath)
		if err != nil {
			return
		}
		defer layerFile.Close()

		err = os.MkdirAll(filepath.Join(imagePath, layer), 0755)
		if err != nil {
			return
		}

		err = tools.TarUnpack(layerPath, filepath.Join(imagePath, layer))
		if err != nil {
			return
		}
	}

	// get the image config
	ociConfigPath := filepath.Join(c.Options.CachePath, digest, manifest.Config)
	ociConfigBytes, err := os.ReadFile(ociConfigPath)
	if err != nil {
		return
	}

	ociConfig = string(ociConfigBytes)
	return
}
