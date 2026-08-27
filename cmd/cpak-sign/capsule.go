/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

func signCapsule(arguments []string) error {
	flags := flag.NewFlagSet("capsule", flag.ContinueOnError)
	installerPath := flags.String("installer", "", "path to the packed installer")
	metadataPath := flags.String("metadata", "", "path to the metadata JSON")
	privateKeyPath := flags.String("private-key", "", "path to the Ed25519 private key")
	outputPath := flags.String("output", "", "output path")
	origin := flags.String("origin", "", "package origin")
	name := flags.String("name", "", "package name")
	description := flags.String("description", "", "package description")
	iconPath := flags.String("icon", "", "path to the package SVG icon")
	refType := flags.String("ref-type", "", "package reference type")
	ref := flags.String("ref", "", "package reference")
	manifestDigest := flags.String("manifest-digest", "", "digest of the package manifest")
	arch := flags.String("arch", runtime.GOARCH, "target architecture")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *installerPath == "" || *privateKeyPath == "" || *outputPath == "" {
		return fmt.Errorf("installer, private-key and output are required")
	}

	installer, err := os.ReadFile(*installerPath)
	if err != nil {
		return err
	}
	metadata, err := capsuleMetadata(*metadataPath, *origin, *name, *description, *iconPath, *refType, *ref, *manifestDigest, *arch)
	if err != nil {
		return err
	}
	privateKey, err := readPrivateKey(*privateKeyPath)
	if err != nil {
		return err
	}
	packed, err := bootstrap.SignCapsule(installer, metadata, privateKey)
	if err != nil {
		return err
	}
	return os.WriteFile(*outputPath, packed, 0755)
}

func capsuleMetadata(metadataPath, origin, name, description, iconPath, refType, ref, manifestDigest, arch string) (bootstrap.Metadata, error) {
	if metadataPath != "" {
		encoded, err := os.ReadFile(metadataPath)
		if err != nil {
			return bootstrap.Metadata{}, err
		}
		var metadata bootstrap.Metadata
		if err = json.Unmarshal(encoded, &metadata); err != nil {
			return bootstrap.Metadata{}, err
		}
		return metadata, nil
	}
	if origin == "" || name == "" || description == "" {
		return bootstrap.Metadata{}, fmt.Errorf("metadata or origin, name and description are required")
	}
	icon := ""
	if iconPath != "" {
		encoded, err := os.ReadFile(iconPath)
		if err != nil {
			return bootstrap.Metadata{}, err
		}
		icon = string(encoded)
	}
	return bootstrap.Metadata{
		Schema:         bootstrap.SchemaVersion,
		Origin:         origin,
		Name:           name,
		Description:    description,
		IconSVG:        icon,
		RefType:        refType,
		Ref:            ref,
		ManifestDigest: manifestDigest,
		Arch:           arch,
	}, nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bootstrap.ParsePrivateKeyPEM(encoded)
}
