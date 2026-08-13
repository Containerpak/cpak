/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
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

func main() {
	installerPath := flag.String("installer", "", "path to the packed installer")
	metadataPath := flag.String("metadata", "", "path to the metadata JSON")
	privateKeyPath := flag.String("private-key", "", "path to the Ed25519 private key")
	outputPath := flag.String("output", "", "output path")
	origin := flag.String("origin", "", "package origin")
	name := flag.String("name", "", "package name")
	description := flag.String("description", "", "package description")
	iconPath := flag.String("icon", "", "path to the package SVG icon")
	refType := flag.String("ref-type", "", "package reference type")
	ref := flag.String("ref", "", "package reference")
	arch := flag.String("arch", runtime.GOARCH, "target architecture")
	flag.Parse()
	if *installerPath == "" || *privateKeyPath == "" || *outputPath == "" {
		fail(fmt.Errorf("installer, private-key and output are required"))
	}

	installer, err := os.ReadFile(*installerPath)
	if err != nil {
		fail(err)
	}
	var metadata bootstrap.Metadata
	if *metadataPath != "" {
		encoded, readErr := os.ReadFile(*metadataPath)
		if readErr != nil {
			fail(readErr)
		}
		if err = json.Unmarshal(encoded, &metadata); err != nil {
			fail(err)
		}
	} else {
		if *origin == "" || *name == "" || *description == "" {
			fail(fmt.Errorf("metadata or origin, name and description are required"))
		}
		icon := ""
		if *iconPath != "" {
			encoded, readErr := os.ReadFile(*iconPath)
			if readErr != nil {
				fail(readErr)
			}
			icon = string(encoded)
		}
		metadata = bootstrap.Metadata{
			Schema:      bootstrap.SchemaVersion,
			Origin:      *origin,
			Name:        *name,
			Description: *description,
			IconSVG:     icon,
			RefType:     *refType,
			Ref:         *ref,
			Arch:        *arch,
		}
	}
	privateKey, err := readPrivateKey(*privateKeyPath)
	if err != nil {
		fail(err)
	}
	packed, err := bootstrap.SignCapsule(installer, metadata, privateKey)
	if err != nil {
		fail(err)
	}
	if err = os.WriteFile(*outputPath, packed, 0755); err != nil {
		fail(err)
	}
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bootstrap.ParsePrivateKeyPEM(encoded)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
