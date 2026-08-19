/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	defaultManifestPath = "cpak.json"
	defaultLockName     = "cpak.lock.json"
	defaultStatePath    = "cpak-state"
	lockLimit           = 8 << 20
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func buildState(arguments []string) error {
	flags := flag.NewFlagSet("state", flag.ContinueOnError)
	manifestPath := flags.String("manifest", defaultManifestPath, "path to the package manifest")
	lockPath := flags.String("lock", "", "path to the lock file, defaults to cpak.lock.json beside the manifest when it exists")
	origin := flags.String("origin", "", "package origin, the repository the manifest is published from")
	image := flags.String("image", "", "image reference to resolve, defaults to the image the manifest declares")
	imageDigest := flags.String("image-digest", "", "image digest to sign, for a registry this run cannot reach")
	generation := flags.Uint64("generation", 0, "generation of this state, higher than the last one published")
	outputPath := flags.String("output", defaultStatePath, "path the payload is written to, - for standard output")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	packageOrigin, err := normalizeOrigin(*origin)
	if err != nil {
		return err
	}
	if *generation == 0 {
		return errors.New("generation is required and starts at 1, so that a state can never be replaced by an older one")
	}

	manifest, manifestSHA256, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	lockSHA256, err := readLock(inferredLockPath(*manifestPath, *lockPath), manifestSHA256)
	if err != nil {
		return err
	}
	resolved, err := resolveImageDigest(context.Background(), manifest, *image, *imageDigest)
	if err != nil {
		return err
	}

	state := signature.State{
		ABI:            signature.ABIVersion,
		Origin:         packageOrigin,
		ManifestSHA256: manifestSHA256,
		ImageDigest:    resolved,
		LockSHA256:     lockSHA256,
		Generation:     *generation,
	}
	if err = state.Validate(); err != nil {
		return fmt.Errorf("build the package state: %w", err)
	}
	payload, err := state.Canonical()
	if err != nil {
		return fmt.Errorf("encode the package state: %w", err)
	}
	if err = writePayload(*outputPath, payload); err != nil {
		return err
	}
	stateDigest, err := state.Digest()
	if err != nil {
		return fmt.Errorf("digest the package state: %w", err)
	}
	fmt.Fprintf(os.Stderr, "state %s: %s at %s, generation %d\n", stateDigest, packageOrigin, resolved, *generation)
	return nil
}

// readManifest returns the manifest and the hash the signed state names it by.
//
// Validation runs first because it is what fills the defaults in. It leaves the
// manifest otherwise as the publisher wrote it, and the installing side hashes
// it at exactly this point, so the two name one manifest. A hash taken before
// validation would miss the defaults; one taken after anything rewrote the
// manifest would name a package nobody published.
func readManifest(path string) (*types.CpakManifest, string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read the manifest: %w", err)
	}
	manifest, err := cpak.DecodeManifest(content)
	if err != nil {
		return nil, "", fmt.Errorf("decode %s: %w", path, err)
	}
	// Validation reads the manifest and nothing else, so it needs no store.
	if err = (&cpak.Cpak{}).ValidateManifest(manifest); err != nil {
		return nil, "", fmt.Errorf("validate %s: %w", path, err)
	}
	digest, err := canonicalDigest(manifest)
	if err != nil {
		return nil, "", fmt.Errorf("hash %s: %w", path, err)
	}
	return manifest, digest, nil
}

// readLock returns the hash of the lock, and the empty string when the package
// has none. A lock built from another manifest is refused rather than signed:
// the two would name different images and only one of them can be installed.
func readLock(path, manifestSHA256 string) (string, error) {
	if path == "" {
		return "", nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the lock: %w", err)
	}
	if len(content) > lockLimit {
		return "", fmt.Errorf("the lock at %s is too large", path)
	}
	var lock types.ManifestLock
	if err = json.Unmarshal(content, &lock); err != nil {
		return "", fmt.Errorf("decode %s: %w", path, err)
	}
	if lock.Root.ManifestSHA256 != manifestSHA256 {
		return "", fmt.Errorf("the lock at %s was built from another manifest, run cpak lock again", path)
	}
	digest, err := canonicalDigest(lock)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return digest, nil
}

func inferredLockPath(manifestPath, explicit string) string {
	if explicit != "" {
		return explicit
	}
	path := filepath.Join(filepath.Dir(manifestPath), defaultLockName)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// resolveImageDigest turns whatever the publisher has into the digest the
// installing cpak measures. An index resolves to the manifest of one
// architecture, which is the digest that machine records, so a state built here
// covers the architecture cpak-sign is running on and no other.
func resolveImageDigest(ctx context.Context, manifest *types.CpakManifest, image, given string) (string, error) {
	if given != "" {
		if !digestPattern.MatchString(given) {
			return "", fmt.Errorf("refusing to sign %q: what a state carries is the image digest and never a tag, because a tag can be repointed at another image the day after it is signed; pass the digest the push reported, or leave --image-digest out and let cpak-sign resolve the reference", given)
		}
		return given, nil
	}
	reference := image
	if reference == "" {
		reference = manifest.Image
	}
	if reference == "" {
		return "", errors.New("no image to resolve: pass --image with the reference the build pushed")
	}
	if _, err := oci.ParseReference(reference); err != nil {
		return "", err
	}
	client := &oci.Client{Credentials: environmentCredentials{}}
	resolved, err := client.Digest(ctx, reference)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", reference, err)
	}
	if !digestPattern.MatchString(resolved) {
		return "", fmt.Errorf("resolving %s produced %q, which is not a digest", reference, resolved)
	}
	return resolved, nil
}

// canonicalDigest hashes the JSON cpak itself encodes a value as, and never the
// bytes of the file it was read from, so that reformatting a manifest leaves a
// signature valid while changing a permission in it does not.
func canonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// normalizeOrigin puts the origin in the form cpak stores it in, so that the
// identity in the certificate is compared with the same string on both sides.
func normalizeOrigin(value string) (string, error) {
	origin := strings.ToLower(strings.TrimSpace(value))
	origin = strings.TrimSuffix(strings.TrimPrefix(origin, "https://"), "/")
	origin = strings.TrimSuffix(origin, ".git")
	if origin == "" {
		return "", errors.New("origin is required, and is the repository the manifest is published from")
	}
	if strings.Contains(origin, "://") {
		return "", fmt.Errorf("origin %q must carry no protocol", value)
	}
	if strings.Count(origin, "/") < 2 {
		return "", fmt.Errorf("origin %q is not a repository, it should read like github.com/example/app", value)
	}
	return origin, nil
}

func writePayload(path string, payload []byte) error {
	if path == "-" {
		_, err := os.Stdout.Write(payload)
		return err
	}
	if err := os.WriteFile(path, payload, 0644); err != nil {
		return fmt.Errorf("write the payload: %w", err)
	}
	return nil
}
