/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func verifySignedInstaller(origin, commit string, manifest *types.CpakManifest) error {
	capsule, err := readParentInstaller()
	if err != nil {
		return fmt.Errorf("verify the signed installer: %w", err)
	}
	if err = verifySignedInstallerMetadata(capsule.Metadata, origin, commit, manifest); err != nil {
		return err
	}
	payload, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return err
	}
	wanted := sha256.Sum256(capsule.Payload)
	actual := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(actual[:], wanted[:]) != 1 {
		return fmt.Errorf("the signed installer did not embed this cpak binary")
	}
	return nil
}

func verifySignedInstallerMetadata(metadata bootstrap.Metadata, origin, commit string, manifest *types.CpakManifest) error {
	if metadata.Origin != origin || metadata.RefType != "commit" || metadata.Ref != commit {
		return fmt.Errorf("the signed installer does not name this package revision")
	}
	digest, err := cpak.ManifestIdentityDigest(manifest)
	if err != nil {
		return err
	}
	if digest != metadata.ManifestDigest {
		return fmt.Errorf("the fetched manifest does not match the signed installer")
	}
	if len(manifest.Dependencies) > 0 || len(manifest.Sessions) > 0 {
		return fmt.Errorf("signed installers do not support dependencies or login sessions")
	}
	if manifest.ImageRef != "" {
		return fmt.Errorf("signed installers do not accept image_ref")
	}
	reference, err := oci.ParseReference(manifest.Image)
	if err != nil || !reference.IsDigest {
		return fmt.Errorf("signed installers require a digest-pinned image")
	}
	if !reflect.DeepEqual(metadata.Permissions, bootstrap.SummarizePermissions(manifest.Override)) {
		return fmt.Errorf("the signed permission list does not match the fetched manifest")
	}
	return nil
}

func readParentInstaller() (bootstrap.Capsule, error) {
	path := filepath.Join("/proc", fmt.Sprint(os.Getppid()), "exe")
	file, err := os.Open(path)
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	key, err := bootstrap.InstallerPublicKey()
	if err != nil {
		return bootstrap.Capsule{}, err
	}
	return bootstrap.ReadCapsule(file, info.Size(), key)
}
