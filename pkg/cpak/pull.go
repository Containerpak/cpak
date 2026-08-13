/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
	"github.com/schollz/progressbar/v3"
)

// Pull pulls a remote image and unpacks it into the storage folder.
//
// Note: cpak does not offer a standard containers storage, it uses a custom
// storage based on the image layers.
func (c *Cpak) Pull(image string, cpakImageId string) (layers []string, ociConfig string, imageDigest string, err error) {
	return c.pull(image, cpakImageId, "")
}

func (c *Cpak) pull(image string, cpakImageId string, origin string) (layers []string, ociConfig string, imageDigest string, err error) {
	err = tools.ValidateImageName(image)
	if err != nil {
		return
	}

	client := &oci.Client{}
	if origin != "" {
		client.Credentials = registryauth.Provider{Origin: origin, Path: c.Options.RegistryAuthPath}
	}
	img, err := client.Resolve(c.Ctx, image)
	if err != nil {
		return
	}
	imageDigest = img.Digest
	ociConfig = string(img.Config)
	layers, err = c.unpackImageLayers(cpakImageId, client, img.Reference, img.Layers)
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
func (c *Cpak) unpackImageLayers(digest string, client *oci.Client, ref oci.Reference, layerObjs []oci.Descriptor) (layers []string, err error) {
	for _, layer := range layerObjs {
		layerDigest := strings.TrimPrefix(layer.Digest, "sha256:")

		available, err := c.layerAvailable(layerDigest)
		if err != nil {
			return layers, err
		}
		if available {
			logger.Printf("Layer %s already present in the store, skipping..", layerDigest)
			layers = append(layers, layerDigest)
			continue
		}

		err = c.downloadLayer(client, ref, layer, layerDigest)
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

func (c *Cpak) downloadLayer(client *oci.Client, ref oci.Reference, layer oci.Descriptor, digest string) (err error) {
	if supported, partialErr := c.downloadChunkedLayer(client, ref, layer, digest); supported {
		if partialErr == nil {
			return nil
		}
		logger.Printf("Partial layer pull unavailable for %s, downloading the complete layer: %v", digest[:min(12, len(digest))], partialErr)
	}
	layerContent, err := client.Blob(c.Ctx, ref, layer)
	if err != nil {
		return
	}

	defer layerContent.Close()

	layerSize := layer.Size
	layerHash := digest[:min(12, len(digest))]

	bar := progressbar.NewOptions64(layerSize,
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

	dedupStore, err := store.Open(c.daBaDeeStoreOptions())
	if err != nil {
		return
	}
	closed := false
	defer func() {
		if !closed {
			_, _ = dedupStore.GC(c.Ctx)
			_ = dedupStore.Close()
		}
	}()

	limited := &io.LimitedReader{R: layerContent, N: layer.Size}
	stream := io.TeeReader(limited, io.MultiWriter(hash, bar))
	extractErr := unpackLayer(c.Ctx, stream, layer.MediaType, layerInStoreDir, dedupStore)
	received := layer.Size - limited.N
	if limited.N != 0 {
		return fmt.Errorf("layer size mismatch for %s: expected %d, received %d", digest, layer.Size, received)
	}
	extra, copyErr := io.Copy(io.Discard, io.LimitReader(layerContent, 1))
	if copyErr != nil {
		return copyErr
	}
	if extra != 0 {
		return fmt.Errorf("layer size mismatch for %s: expected %d, received more than %d", digest, layer.Size, layer.Size)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return fmt.Errorf("layer digest mismatch for %s", digest)
	}
	if extractErr != nil {
		return extractErr
	}
	if err = dedupStore.Close(); err != nil {
		return err
	}
	closed = true

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
