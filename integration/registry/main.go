/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	manifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	configMediaType   = "application/vnd.oci.image.config.v1+json"
	layerMediaType    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type layerFile struct {
	path string
	mode int64
	data []byte
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type imageManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

type image struct {
	manifest       []byte
	manifestDigest string
	blobs          map[string][]byte
}

type registry struct {
	images map[string]image
}

func main() {
	listen := flag.String("listen", "127.0.0.1:5000", "registry address")
	metadata := flag.String("metadata", "", "path for image digest metadata")
	probe := flag.String("probe", "", "path to the integration probe")
	flag.Parse()
	if !filepath.IsAbs(*probe) {
		log.Fatal("--probe must be an absolute path")
	}
	probeData, err := os.ReadFile(*probe)
	if err != nil {
		log.Fatal(err)
	}
	base := []layerFile{
		{path: "usr/local/bin/cpak-integration-probe", mode: 0755, data: probeData},
		{path: "usr/share/applications/cpak-integration.desktop", mode: 0644, data: []byte("[Desktop Entry]\nType=Application\nName=cpak integration\nExec=/usr/local/bin/cpak-integration-probe desktop\n")},
	}
	files := map[string][]layerFile{
		"probe":      base,
		"dependency": append(append([]layerFile{}, base...), layerFile{path: "opt/cpak-integration/dependency", mode: 0644, data: []byte("present\n")}),
		"addon":      append(append([]layerFile{}, base...), layerFile{path: "opt/cpak-integration/addon", mode: 0644, data: []byte("present\n")}),
	}
	images := make(map[string]image, len(files))
	for name, contents := range files {
		images[name], err = buildImage(contents)
		if err != nil {
			log.Fatal(err)
		}
	}
	if *metadata != "" {
		digests := make(map[string]string, len(images))
		for name, built := range images {
			digests[name] = built.manifestDigest
		}
		encoded, encodeErr := json.Marshal(digests)
		if encodeErr != nil {
			log.Fatal(encodeErr)
		}
		if err = os.WriteFile(*metadata, encoded, 0644); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("integration registry ready")
	log.Fatal(http.ListenAndServe(*listen, registry{images: images}))
}

func buildImage(files []layerFile) (image, error) {
	layer, err := buildLayer(files)
	if err != nil {
		return image{}, err
	}
	config, err := json.Marshal(map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"config": map[string]any{
			"Entrypoint": []string{"/usr/local/bin/cpak-integration-probe"},
			"Env":        []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
		},
	})
	if err != nil {
		return image{}, err
	}
	configDescriptor := describe(config, configMediaType)
	layerDescriptor := describe(layer, layerMediaType)
	manifest, err := json.Marshal(imageManifest{
		SchemaVersion: 2,
		MediaType:     manifestMediaType,
		Config:        configDescriptor,
		Layers:        []descriptor{layerDescriptor},
	})
	if err != nil {
		return image{}, err
	}
	return image{
		manifest:       manifest,
		manifestDigest: digest(manifest),
		blobs: map[string][]byte{
			configDescriptor.Digest: config,
			layerDescriptor.Digest:  layer,
		},
	}, nil
}

func buildLayer(files []layerFile) ([]byte, error) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range files {
		header := &tar.Header{Name: file.path, Mode: file.mode, Size: int64(len(file.data)), ModTime: time.Unix(0, 0)}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tarWriter.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func describe(data []byte, mediaType string) descriptor {
	return descriptor{MediaType: mediaType, Digest: digest(data), Size: int64(len(data))}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r registry) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v2/" {
		writer.WriteHeader(http.StatusOK)
		return
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v2" {
		http.NotFound(writer, request)
		return
	}
	current, ok := r.images[parts[1]]
	if !ok {
		http.NotFound(writer, request)
		return
	}
	switch parts[2] {
	case "manifests":
		if parts[3] != "latest" && parts[3] != current.manifestDigest {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", manifestMediaType)
		writer.Header().Set("Docker-Content-Digest", current.manifestDigest)
		writer.Header().Set("Content-Length", fmt.Sprint(len(current.manifest)))
		if request.Method != http.MethodHead {
			_, _ = writer.Write(current.manifest)
		}
	case "blobs":
		blob, exists := current.blobs[parts[3]]
		if !exists {
			http.NotFound(writer, request)
			return
		}
		http.ServeContent(writer, request, parts[3], time.Unix(0, 0), bytes.NewReader(blob))
	default:
		http.NotFound(writer, request)
	}
}
