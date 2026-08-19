/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

func testStoreOptions(root string) Options {
	options := Options{
		BinPath:       filepath.Join(root, "bin"),
		ManifestsPath: filepath.Join(root, "manifests"),
		ExportsPath:   filepath.Join(root, "exports"),
		StorePath:     filepath.Join(root, "store"),
		CachePath:     filepath.Join(root, "cache"),
		DaBaDeeStoreOptions: store.Options{
			Root: filepath.Join(root, "dabadee"),
		},
	}
	options.StoreLayersPath = filepath.Join(options.StorePath, "fvs", "layers")
	options.StoreContainersPath = filepath.Join(options.StorePath, "containers")
	options.StoreStatesPath = filepath.Join(options.StorePath, "states")
	return options
}

func refuseOpenDirectories(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("%s is open to the rest of the machine: %04o", path, info.Mode().Perm())
		}
	}
}

func TestStoreTreeIsReadableOnlyByItsOwner(t *testing.T) {
	root := t.TempDir()
	options := testStoreOptions(root)
	// The deduplication store is created by its library at 0755, which is the
	// state cpak has to answer for whether or not it made the directory.
	if err := os.MkdirAll(options.DaBaDeeStoreOptions.Root, 0755); err != nil {
		t.Fatal(err)
	}
	createCpakDirs(&options)

	c := &Cpak{Options: options}
	state, err := c.GetInStoreDirMkdir("states", "demo-container")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := c.GetInCacheDirMkdir("manifests", "demo")
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := c.GetInManifestsDirMkdir("github.com/containerpak/demo")
	if err != nil {
		t.Fatal(err)
	}
	database, err := NewStore(options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// The upper directory of the overlay is everything the application wrote,
	// and the deduplication store holds a hardlink to every file of every
	// layer, which is why both ends of the tree are checked.
	refuseOpenDirectories(t,
		options.StorePath,
		options.StoreLayersPath,
		options.StoreStatesPath,
		options.CachePath,
		options.ManifestsPath,
		options.DaBaDeeStoreOptions.Root,
		state,
		filepath.Join(state, "up"),
		filepath.Join(state, "work"),
		cache,
		manifests,
		filepath.Join(options.StorePath, "db"),
		filepath.Join(options.StorePath, "db", "apps"),
		filepath.Join(options.StorePath, "db", "containers"),
	)
}

func TestAnOlderStoreTreeIsTightenedDownToItsLeaves(t *testing.T) {
	root := t.TempDir()
	options := testStoreOptions(root)

	// What an installation made before any of this looks like: every
	// directory created at 0755, including the ones cpak never names.
	existing := []string{
		options.StorePath,
		filepath.Join(options.StorePath, "fvs"),
		options.StoreLayersPath,
		options.StoreStatesPath,
		filepath.Join(options.StoreStatesPath, "demo-container"),
		options.CachePath,
		filepath.Join(options.CachePath, "manifests"),
		options.ManifestsPath,
		filepath.Join(options.ManifestsPath, "github.com", "containerpak"),
		options.DaBaDeeStoreOptions.Root,
	}
	for _, path := range existing {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	createCpakDirs(&options)
	c := &Cpak{Options: options}
	state, err := c.GetInStoreDirMkdir("states", "demo-container")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := c.GetInCacheDirMkdir("manifests", "demo")
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := c.GetInManifestsDirMkdir("github.com/containerpak/demo")
	if err != nil {
		t.Fatal(err)
	}

	refuseOpenDirectories(t, append(existing,
		state,
		filepath.Join(state, "up"),
		cache,
		manifests,
	)...)
}

func TestAuditTightensWhatTheStoreHelpersNeverWalk(t *testing.T) {
	c := newTestCpak(t)
	c.Options.DaBaDeeStoreOptions.Root = filepath.Join(filepath.Dir(c.Options.StorePath), "dabadee")

	// The deduplication store is created by its library and the exports tree
	// by the installer: neither is a directory the store helpers walk.
	deduplicated := c.Options.DaBaDeeStoreOptions.Root
	exported := filepath.Join(c.Options.ExportsPath, "github.com", "containerpak")
	// A container rootfs is the image's own tree, and the audit has no
	// business deciding what the application sees inside it.
	rootfs := c.GetInStoreDir("containers", "demo-container", "rootfs", "usr", "bin")
	for _, path := range []string{deduplicated, exported, rootfs} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Audit(true); err != nil {
		t.Fatal(err)
	}

	refuseOpenDirectories(t, deduplicated, exported, filepath.Dir(exported))
	info, err := os.Stat(rootfs)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("the audit rewrote what a container rootfs carries: %04o", info.Mode().Perm())
	}
}

func TestAPrivateDirectoryOwnedByAnotherUserSaysWhoseItIs(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("every directory is owned by root when the test is")
	}
	foreign := ""
	for _, candidate := range []string{"/root", "/usr", "/etc"} {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid == uint32(os.Getuid()) {
			continue
		}
		foreign = candidate
		break
	}
	if foreign == "" {
		t.Skip("no directory owned by another user to check against")
	}

	err := securePrivateDirectory(foreign)
	if err == nil {
		t.Fatalf("%s was accepted as this user's private directory", foreign)
	}
	// A single sudo run leaves such a directory behind and every later cpak
	// command dies on it, so the message has to carry the whole remedy.
	message := err.Error()
	for _, expected := range []string{foreign, "uid " + strconv.Itoa(os.Getuid()), "chown"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("the refusal does not say %q: %s", expected, message)
		}
	}
}

// A directory this user cannot make private is a hygiene problem. It used to
// be a kill switch: securePrivateDirectory runs inside getCpakOptions, which
// every command builds its options with, so one directory left behind by a
// sudo run stopped cpak list, cpak uninstall and cpak audit --repair alike,
// and the repair was the command the user had been told to run.
//
// A plain file where a directory belongs is the same refusal from
// securePrivateDirectory as a foreign owner, and it does not need a second uid
// to arrange.
func TestOneDirectoryThatCannotBeSecuredDoesNotStopCpak(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CPAK_INSTALLATION_PATH", root)
	t.Setenv("CPAK_OPTS_FILE", filepath.Join(root, "no-such-configuration.json"))
	if err := os.WriteFile(filepath.Join(root, "bin"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	options, err := getCpakOptions()
	if err != nil {
		t.Fatalf("cpak did not start because one directory could not be secured: %v", err)
	}

	// The rest of the tree was still made, and still made private.
	refuseOpenDirectories(t,
		options.StorePath,
		options.StoreLayersPath,
		options.StoreStatesPath,
		options.CachePath,
		options.ManifestsPath,
	)

	// And the pass that tightens an older tree, which is what the user is
	// pointed at, runs.
	c := &Cpak{Options: options}
	if err := c.auditPrivateTree(true); err != nil {
		t.Fatalf("the repair pass could not run: %v", err)
	}
}
