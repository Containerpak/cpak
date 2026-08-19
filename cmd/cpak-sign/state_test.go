/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const testOrigin = "github.com/example/app"

func writeManifest(t *testing.T, directory, image string) string {
	t.Helper()
	path := filepath.Join(directory, defaultManifestPath)
	content := fmt.Sprintf(`{
  "manifest_version": "2.0",
  "name": "Example",
  "description": "An example package",
  "image": %q,
  "binaries": ["/usr/bin/example"]
}`, image)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing the manifest fixture failed: %v", err)
	}
	return path
}

// readStateFile reads a payload back through the same reader attach uses, which
// is what holds a written payload to the property the signature depends on: the
// bytes on disk are the canonical encoding of the state they carry, because
// those bytes are what gets signed.
func readStateFile(t *testing.T, path string) signature.State {
	t.Helper()
	state, err := readSignedState(path)
	if err != nil {
		t.Fatalf("the payload cannot be read back as the state it encodes: %v", err)
	}
	return state
}

func TestStateRefusesATagAsTheImageDigest(t *testing.T) {
	directory := t.TempDir()
	manifest := writeManifest(t, directory, "ghcr.io/example/app:main")
	output := filepath.Join(directory, defaultStatePath)

	err := buildState([]string{
		"--manifest", manifest,
		"--origin", testOrigin,
		"--generation", "1",
		"--image-digest", "main",
		"--output", output,
	})
	if err == nil {
		t.Fatalf("a tag was accepted as the thing to sign, which a repointed tag would then invalidate silently")
	}
	// The refusal has to be cpak-sign's own and has to name the tag: a state
	// rejected later, for an image digest that does not look like one, would
	// leave the publisher guessing what a tag has to do with it.
	if !strings.Contains(err.Error(), "never a tag") || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("the refusal does not say that the digest, and never the tag, is what is signed: %v", err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("a payload was written for a reference that is not a digest")
	}
}

func TestStateSignsTheDigestTheTagResolvesTo(t *testing.T) {
	registry := newFakeRegistry(t)
	imageDigest := registry.publishImage("main")
	directory := t.TempDir()
	manifest := writeManifest(t, directory, registry.reference("main"))
	output := filepath.Join(directory, defaultStatePath)

	if err := buildState([]string{
		"--manifest", manifest,
		"--origin", testOrigin,
		"--generation", "7",
		"--output", output,
	}); err != nil {
		t.Fatalf("building the state failed: %v", err)
	}

	state := readStateFile(t, output)
	if state.ImageDigest != imageDigest {
		t.Fatalf("the state names %s, and the tag resolves to %s", state.ImageDigest, imageDigest)
	}
	if indexDigest := digestOf(registry.manifests["main"]); state.ImageDigest == indexDigest {
		t.Fatalf("the state names the index %s and not the manifest an installation on this architecture measures", indexDigest)
	}
	if state.Origin != testOrigin || state.Generation != 7 {
		t.Fatalf("the state carries %s at generation %d", state.Origin, state.Generation)
	}
	if state.LockSHA256 != "" {
		t.Fatalf("a package with no lock was signed with a lock hash: %s", state.LockSHA256)
	}
}

func TestStateRefusesALockBuiltFromAnotherManifest(t *testing.T) {
	directory := t.TempDir()
	manifest := writeManifest(t, directory, "ghcr.io/example/app@sha256:"+strings.Repeat("b", 64))
	lock := fmt.Sprintf(`{"lock_version":"1.0","root":{"manifest_sha256":%q,"image":"ghcr.io/example/app","resolved_image":"ghcr.io/example/app"},"dependencies":[]}`, strings.Repeat("a", 64))
	if err := os.WriteFile(filepath.Join(directory, defaultLockName), []byte(lock), 0o644); err != nil {
		t.Fatalf("writing the lock fixture failed: %v", err)
	}
	output := filepath.Join(directory, defaultStatePath)

	err := buildState([]string{
		"--manifest", manifest,
		"--origin", testOrigin,
		"--generation", "1",
		"--image-digest", "sha256:" + strings.Repeat("c", 64),
		"--output", output,
	})
	if err == nil {
		t.Fatalf("a lock describing another manifest was signed, so the state would name two different images")
	}
	if !strings.Contains(err.Error(), "cpak lock") {
		t.Fatalf("the refusal does not say how to rebuild the lock: %v", err)
	}
}

func TestStateHashesTheLockBesideTheManifest(t *testing.T) {
	directory := t.TempDir()
	manifest := writeManifest(t, directory, "ghcr.io/example/app@sha256:"+strings.Repeat("b", 64))
	_, manifestSHA256, err := readManifest(manifest)
	if err != nil {
		t.Fatalf("reading the manifest fixture failed: %v", err)
	}
	content := fmt.Sprintf(`{"lock_version":"1.0","root":{"manifest_sha256":%q,"image":"ghcr.io/example/app","resolved_image":"ghcr.io/example/app"},"dependencies":[]}`, manifestSHA256)
	if err = os.WriteFile(filepath.Join(directory, defaultLockName), []byte(content), 0o644); err != nil {
		t.Fatalf("writing the lock fixture failed: %v", err)
	}
	output := filepath.Join(directory, defaultStatePath)

	if err = buildState([]string{
		"--manifest", manifest,
		"--origin", testOrigin,
		"--generation", "2",
		"--image-digest", "sha256:" + strings.Repeat("c", 64),
		"--output", output,
	}); err != nil {
		t.Fatalf("building the state failed: %v", err)
	}

	var lock types.ManifestLock
	if err = json.Unmarshal([]byte(content), &lock); err != nil {
		t.Fatalf("decoding the lock fixture failed: %v", err)
	}
	expected, err := canonicalDigest(lock)
	if err != nil {
		t.Fatalf("hashing the lock fixture failed: %v", err)
	}
	if state := readStateFile(t, output); state.LockSHA256 != expected {
		t.Fatalf("the state names the lock %s and the lock hashes to %s", state.LockSHA256, expected)
	}
}

// TestStateNamesWhatTheInstallingSideNames is the interoperability the whole
// design rests on. The state a publisher signs and the state a machine rebuilds
// out of what it installed have to be the same tuple, hashed the same way, or
// every signature is refused for a reason nobody can see from either side.
func TestStateNamesWhatTheInstallingSideNames(t *testing.T) {
	directory := t.TempDir()
	imageDigest := "sha256:" + strings.Repeat("c", 64)
	manifestPath := writeManifest(t, directory, "ghcr.io/example/app@sha256:"+strings.Repeat("b", 64))
	manifest, manifestSHA256, err := readManifest(manifestPath)
	if err != nil {
		t.Fatalf("reading the manifest fixture failed: %v", err)
	}
	content := fmt.Sprintf(`{"lock_version":"1.0","root":{"manifest_sha256":%q,"image":"ghcr.io/example/app","resolved_image":"ghcr.io/example/app"},"dependencies":[]}`, manifestSHA256)
	if err = os.WriteFile(filepath.Join(directory, defaultLockName), []byte(content), 0o644); err != nil {
		t.Fatalf("writing the lock fixture failed: %v", err)
	}
	output := filepath.Join(directory, defaultStatePath)

	if err = buildState([]string{
		"--manifest", manifestPath,
		"--origin", testOrigin,
		"--generation", "5",
		"--image-digest", imageDigest,
		"--output", output,
	}); err != nil {
		t.Fatalf("building the state failed: %v", err)
	}
	signed := readStateFile(t, output)

	var lock types.ManifestLock
	if err = json.Unmarshal([]byte(content), &lock); err != nil {
		t.Fatalf("decoding the lock fixture failed: %v", err)
	}
	installed, err := cpak.PackageState(testOrigin, manifest, imageDigest, &lock)
	if err != nil {
		t.Fatalf("the installing side cannot name this state: %v", err)
	}
	// The generation is the one field an installation cannot derive: it is the
	// publisher's counter and it travels with the signature.
	installed.Generation = signed.Generation
	if installed != signed {
		t.Fatalf("the publisher signs %+v and the installing side rebuilds %+v", signed, installed)
	}
}

func TestStateRequiresAGeneration(t *testing.T) {
	directory := t.TempDir()
	manifest := writeManifest(t, directory, "ghcr.io/example/app@sha256:"+strings.Repeat("b", 64))

	err := buildState([]string{
		"--manifest", manifest,
		"--origin", testOrigin,
		"--image-digest", "sha256:" + strings.Repeat("c", 64),
		"--output", filepath.Join(directory, defaultStatePath),
	})
	if err == nil {
		t.Fatalf("a state without a generation was accepted, so nothing orders it against the last one published")
	}
	if !strings.Contains(err.Error(), "generation") {
		t.Fatalf("the refusal does not name the generation: %v", err)
	}
}

func TestNormalizeOrigin(t *testing.T) {
	for _, testCase := range []struct {
		value    string
		expected string
		refused  bool
	}{
		{value: "github.com/example/app", expected: testOrigin},
		{value: "https://GitHub.com/Example/App.git", expected: testOrigin},
		{value: "github.com/example/app/", expected: testOrigin},
		{value: "", refused: true},
		{value: "ssh://github.com/example/app", refused: true},
		{value: "example/app", refused: true},
	} {
		origin, err := normalizeOrigin(testCase.value)
		if testCase.refused {
			if err == nil {
				t.Fatalf("%q was accepted as an origin and read as %q", testCase.value, origin)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q was refused as an origin: %v", testCase.value, err)
		}
		if origin != testCase.expected {
			t.Fatalf("%q became %q and not %q", testCase.value, origin, testCase.expected)
		}
	}
}

// TestTheStateOfAV1PackageNamesTheManifestAsPublished is the signing side of
// the digest boundary. What a signed state binds is the hash of the manifest,
// taken once validation has filled the defaults in, and the installing cpak
// takes the same hash the same way. A manifest that came back from validation
// migrated would be hashed here as something the publisher never wrote, and
// every state already signed for a v1 package would stop binding.
func TestTheStateOfAV1PackageNamesTheManifestAsPublished(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cpak.json")
	body := `{"manifest_version":"1.0","name":"Demo","description":"Demo application","image":"ghcr.io/example/app:latest","binaries":["/usr/bin/demo"],"override":{"fsHostHome":true,"fsExtra":["/srv/data"]}}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	published, err := cpak.DecodeManifest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonicalDigest(published)
	if err != nil {
		t.Fatal(err)
	}
	manifest, got, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the signed digest of a v1 manifest moved: %s became %s", want, got)
	}
	if manifest.ManifestVersion != "1.0" || !manifest.Override.FsHostHome {
		t.Fatalf("the manifest signing read was rewritten: %s %+v", manifest.ManifestVersion, manifest.Override)
	}
}
