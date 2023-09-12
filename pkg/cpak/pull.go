package cpak

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/schollz/progressbar/v3"
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

	// getting the image config
	ociConfigObj, err := img.ConfigFile()
	if err != nil {
		return
	}

	ociConfigBytes, err := json.Marshal(ociConfigObj)
	if err != nil {
		return
	}

	ociConfig = string(ociConfigBytes)

	// unpacking the image layers into the storage/images folder
	layerObjs, err := img.Layers()
	if err != nil {
		return
	}

	layers, err = c.unpackImageLayers(cpakImageId, img, layerObjs)
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
func (c *Cpak) unpackImageLayers(digest string, image v1.Image, layerObjs []v1.Layer) (layers []string, err error) {
	availableLayers, err := c.GetAvailableLayers()
	if err != nil {
		return
	}

	for _, layer := range layerObjs {
		layerDigest, err := layer.Digest()
		if err != nil {
			return layers, err
		}

		if _, ok := availableLayers[layerDigest.String()]; !ok {
			err = c.downloadLayer(image, layer)
			if err != nil {
				return layers, err
			}
		}

		layers = append(layers, layerDigest.String())
	}

	return
}

func (c *Cpak) GetAvailableLayers() (layers map[string]string, err error) {
	layers = make(map[string]string)

	err = filepath.Walk(c.Options.StoreLayersPath, func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		layerHash := filepath.Base(path)
		layerHash = strings.TrimSuffix(layerHash, filepath.Ext(layerHash))
		layers[layerHash] = path

		return nil
	})

	return
}

func (c *Cpak) ensureApplicationLayers(layers []string) (err error) {
	availableLayers, err := c.GetAvailableLayers()
	if err != nil {
		return
	}

	for _, layer := range layers {
		if _, ok := availableLayers[layer]; !ok {
			return fmt.Errorf("layer %s not found", layer)
		}
	}

	return
}

func (c *Cpak) downloadLayer(image v1.Image, layer v1.Layer) (err error) {
	digest, err := layer.Digest()
	if err != nil {
		return
	}

	layerInCacheDir := c.GetInCacheDir(digest.String())
	layerContent, err := layer.Compressed()
	if err != nil {
		return
	}

	defer layerContent.Close()

	layerFile, err := os.Create(layerInCacheDir)
	if err != nil {
		return
	}

	defer layerFile.Close()

	layerSize, err := layer.Size()
	if err != nil {
		return
	}

	hash := digest.String()
	if strings.Contains(hash, ":") {
		hash = hash[strings.Index(hash, ":")+1:]
	}
	hash = hash[:12]

	bar := progressbar.NewOptions(int(layerSize),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "━",
			SaucerHead:    "╸",
			SaucerPadding: " ",
			BarStart:      "",
			BarEnd:        "",
		}),
		// the following add a new line after the progress bar
		progressbar.OptionSetWriter(io.MultiWriter(os.Stderr, os.Stderr)),
		progressbar.OptionSetDescription("Downloading "+hash),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionFullWidth(),
	)
	writer := io.MultiWriter(layerFile, bar)

	_, err = io.Copy(writer, layerContent)
	if err != nil {
		return
	}

	layerInStoreDir, err := c.GetInStoreDirMkdir("layers", digest.String())
	if err != nil {
		return
	}

	err = tools.TarUnpack(layerInCacheDir, layerInStoreDir)
	return
}
