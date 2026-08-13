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

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/tools"
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
	return c.ensureFVSLayer(digest)
}

func (c *Cpak) GetAvailableLayers() (layers []string, err error) {
	if err := c.migrateLegacyLayers(); err != nil {
		return nil, err
	}
	layersDir := c.fvsLayersPath()

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
	layerContent, err := client.ResumableBlob(c.Ctx, ref, layer)
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
	layerInStoreDir, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "pull " + digest,
		ComputeSHA256: true,
	})
	if err != nil {
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = writer.Abort()
		}
		_ = os.RemoveAll(layerInStoreDir)
	}()

	limited := &io.LimitedReader{R: layerContent, N: layer.Size}
	stream := io.TeeReader(limited, io.MultiWriter(hash, bar))
	extractErr := unpackLayer(c.Ctx, stream, layer.MediaType, writer)
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
	if _, err = writer.Commit(); err != nil {
		return err
	}
	committed = true
	if err = publishFVSLayer(layerInStoreDir, c.fvsLayerPath(digest)); err != nil {
		return
	}

	return
}
