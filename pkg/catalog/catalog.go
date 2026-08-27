/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package catalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

const DefaultURL = "https://github.com/Containerpak/cpak/releases/latest/download/cpak-installer-catalog.json"

const maxCatalogSize = 16 * 1024 * 1024

type Package struct {
	Metadata    bootstrap.Metadata
	Release     string
	Installable bool
}

type signedEntry struct {
	Metadata  string `json:"metadata"`
	Signature string `json:"signature"`
}

type document struct {
	Schema   int                               `json:"schema"`
	Release  string                            `json:"release"`
	Packages map[string]map[string]signedEntry `json:"packages"`
}

func Fetch(ctx context.Context, client *http.Client, address, arch string, publicKey ed25519.PublicKey) ([]Package, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if address == "" {
		address = DefaultURL
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogSize+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxCatalogSize {
		return nil, errors.New("catalog exceeds the size limit")
	}
	return Decode(encoded, arch, publicKey)
}

func Decode(encoded []byte, arch string, publicKey ed25519.PublicKey) ([]Package, error) {
	var catalog document
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if catalog.Schema != 1 || catalog.Release == "" || len(catalog.Packages) == 0 {
		return nil, errors.New("catalog header is invalid")
	}
	packages := make([]Package, 0, len(catalog.Packages))
	for origin, architectures := range catalog.Packages {
		entry, ok := architectures[arch]
		if !ok {
			continue
		}
		metadata, err := verifyEntry(entry, publicKey)
		if err != nil {
			return nil, fmt.Errorf("verify %s: %w", origin, err)
		}
		if metadata.Origin != origin || metadata.Arch != arch {
			return nil, fmt.Errorf("catalog identity mismatch for %s", origin)
		}
		packages = append(packages, Package{
			Metadata:    metadata,
			Release:     catalog.Release,
			Installable: metadata.Schema == bootstrap.SchemaVersion,
		})
	}
	sort.Slice(packages, func(i, j int) bool {
		return strings.ToLower(packages[i].Metadata.Name) < strings.ToLower(packages[j].Metadata.Name)
	})
	return packages, nil
}

func Find(packages []Package, origin string) (Package, error) {
	for _, item := range packages {
		if item.Metadata.Origin == origin {
			return item, nil
		}
	}
	return Package{}, fmt.Errorf("package is not present in the signed catalog: %s", origin)
}

func verifyEntry(entry signedEntry, publicKey ed25519.PublicKey) (bootstrap.Metadata, error) {
	metadataJSON, err := base64.StdEncoding.DecodeString(entry.Metadata)
	if err != nil {
		return bootstrap.Metadata{}, errors.New("metadata is not valid base64")
	}
	signature, err := base64.StdEncoding.DecodeString(entry.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return bootstrap.Metadata{}, errors.New("signature is invalid")
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, metadataJSON, signature) {
		return bootstrap.Metadata{}, errors.New("metadata signature is invalid")
	}
	var metadata bootstrap.Metadata
	decoder := json.NewDecoder(bytes.NewReader(metadataJSON))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&metadata); err != nil {
		return bootstrap.Metadata{}, fmt.Errorf("decode metadata: %w", err)
	}
	if err = requireJSONEnd(decoder); err != nil {
		return bootstrap.Metadata{}, fmt.Errorf("decode metadata: %w", err)
	}
	if metadata.Schema == bootstrap.SchemaVersion {
		return metadata, metadata.Validate()
	}
	if metadata.Schema != 1 {
		return bootstrap.Metadata{}, fmt.Errorf("unsupported metadata schema: %d", metadata.Schema)
	}
	if err = validateLegacyMetadata(metadata); err != nil {
		return bootstrap.Metadata{}, err
	}
	return metadata, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func validateLegacyMetadata(metadata bootstrap.Metadata) error {
	parts := strings.Split(metadata.Origin, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return errors.New("legacy metadata origin is invalid")
	}
	if metadata.Name == "" || metadata.Description == "" || metadata.RefType != "commit" {
		return errors.New("legacy metadata identity is invalid")
	}
	commit, err := hex.DecodeString(metadata.Ref)
	if err != nil || len(commit) != 20 {
		return errors.New("legacy metadata commit is invalid")
	}
	if metadata.Arch != "amd64" && metadata.Arch != "arm64" {
		return errors.New("legacy metadata architecture is invalid")
	}
	if len(metadata.Permissions) > 32 {
		return errors.New("legacy metadata permission list is too long")
	}
	return nil
}
