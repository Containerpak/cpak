/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/schollz/progressbar/v3"
)

// Pull pulls a remote image and unpacks it into the storage folder.
//
// Note: cpak does not offer a standard containers storage, it uses a custom
// storage based on the image layers.
func (c *Cpak) Pull(image string, cpakImageId string) (layers []string, ociConfig string, imageDigest string, err error) {
	err = tools.ValidateImageName(image)
	if err != nil {
		return
	}

	// getting the v1.Image of the remote image
	img, err := crane.Pull(image, crane.WithContext(c.Ctx))
	if err != nil {
		return
	}
	imageHash, err := img.Digest()
	if err != nil {
		return
	}
	imageDigest = imageHash.String()

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
// and returns the list of layers.
//
// Note: only the layers that are not already present in the storage are
// downloaded and unpacked.
func (c *Cpak) unpackImageLayers(digest string, image v1.Image, layerObjs []v1.Layer) (layers []string, err error) {
	for _, layer := range layerObjs {
		layerv1Hash, err := layer.Digest()
		if err != nil {
			return layers, err
		}
		layerDigest := strings.Split(layerv1Hash.String(), ":")[1]

		available, err := c.layerAvailable(layerDigest)
		if err != nil {
			return layers, err
		}
		if available {
			logger.Printf("Layer %s already present in the store, skipping..", layerDigest)
			layers = append(layers, layerDigest)
			continue
		}

		err = c.downloadLayer(image, layer, layerDigest)
		if err != nil {
			return layers, err
		}

		layers = append(layers, layerDigest)
	}

	return
}

func (c *Cpak) layerAvailable(digest string) (bool, error) {
	info, err := os.Stat(c.GetInStoreDir("layers", digest))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (c *Cpak) GetAvailableLayers() (layers []string, err error) {
	layersDir := c.GetInStoreDir("layers")

	_, err = os.Stat(layersDir)
	if err != nil {
		return nil, err
	}

	files, err := os.ReadDir(layersDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if file.IsDir() {
			layers = append(layers, filepath.Join(layersDir, file.Name()))
		}
	}

	return layers, nil
}

// func (c *Cpak) ensureApplicationLayers(layers []string) (err error) {
// 	availableLayers, err := c.GetAvailableLayers()
// 	if err != nil {
// 		return
// 	}

// 	for _, layer := range layers {
// 		found := false
// 		for _, a := range availableLayers {
// 			if strings.Contains(a, layer) {
// 				found = true
// 				break
// 			}
// 		}

// 		if !found {
// 			return fmt.Errorf("layer %s not found", layer)
// 		}
// 	}

// 	return
// }

func (c *Cpak) downloadLayer(image v1.Image, layer v1.Layer, digest string) (err error) {
	if _, err = c.GetInCacheDirMkdir(); err != nil {
		return err
	}
	layerFile, err := os.CreateTemp(c.Options.CachePath, digest+".partial-")
	if err != nil {
		return err
	}
	layerInCacheDir := layerFile.Name()
	defer os.Remove(layerInCacheDir)
	layerContent, err := layer.Compressed()
	if err != nil {
		return
	}

	defer layerContent.Close()

	layerSize, err := layer.Size()
	if err != nil {
		return
	}

	layerHash := digest[strings.Index(digest, ":")+1:][:12]

	bar := progressbar.NewOptions(int(layerSize),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "━",
			SaucerHead:    "╸",
			SaucerPadding: " ",
			BarStart:      "",
			BarEnd:        "",
		}),
		// the following add a new line after the progress bar
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetDescription("Downloading "+layerHash),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionFullWidth(),
	)
	hash := sha256.New()
	writer := io.MultiWriter(layerFile, hash, bar)

	_, err = io.Copy(writer, layerContent)
	if err != nil {
		return
	}
	if err = layerFile.Close(); err != nil {
		return
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return fmt.Errorf("layer digest mismatch for %s", digest)
	}

	layersDir, err := c.GetInStoreDirMkdir("layers")
	if err != nil {
		return
	}
	layerInStoreDir, err := os.MkdirTemp(layersDir, digest+".partial-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(layerInStoreDir)
		}
	}()

	err = tools.TarUnpack(layerInCacheDir, layerInStoreDir)
	if err != nil {
		return
	}

	// dabadee deduplication is performed on a new namespace to avoid
	// permission issues
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	cmds := []string{}
	if isVerbose {
		cmds = append(cmds, "--debug")
	}
	cmds = append(cmds, "dedup")
	if isVerbose {
		cmds = append(cmds, "--verbose")
	}
	cmds = append(cmds, "--path", layerInStoreDir)
	cmd := exec.Command(cpakBinary, cmds...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return
	}

	if err = c.publishLayer(layerInStoreDir, digest); err != nil {
		return
	}

	return
}

func (c *Cpak) publishLayer(source, digest string) error {
	target := c.GetInStoreDir("layers", digest)
	if err := os.Rename(source, target); err == nil {
		return nil
	} else if available, checkErr := c.layerAvailable(digest); checkErr != nil {
		return checkErr
	} else if !available {
		return err
	}

	return os.RemoveAll(source)
}
