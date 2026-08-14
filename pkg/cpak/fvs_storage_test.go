/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

func seedFVSLayerFile(t testing.TB, cp *Cpak, digest, name string, content []byte) {
	t.Helper()
	repository, err := fvsrepo.InitWithOptions(cp.fvsLayerPath(digest), fvsrepo.InitOptions{BlocksPath: cp.fvsBlocksPath()})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Add(fvsrepo.Entry{Path: name, Kind: fvsrepo.EntryFile, Mode: 0o644, Size: int64(len(content))}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
}

func readFVSLayerFile(t *testing.T, cp *Cpak, digest, name string) []byte {
	t.Helper()
	states, err := fvsrepo.States(cp.fvsLayerPath(digest))
	if err != nil || len(states) == 0 {
		t.Fatalf("read layer state: %v", err)
	}
	destination := t.TempDir()
	if _, err := fvsrepo.Restore(cp.fvsLayerPath(digest), states[0].ID, fvsrepo.RestoreOptions{To: destination}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestEnsureFVSLayerMigratesLegacyDirectory(t *testing.T) {
	cp := newTestCpak(t)
	digest := "legacy"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(filepath.Join(legacy, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "usr", "share", "value"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	available, err := cp.ensureFVSLayer(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("migrated layer is unavailable")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy layer still present: %v", err)
	}
	states, err := fvsrepo.States(cp.fvsLayerPath(digest))
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %d, err = %v", len(states), err)
	}
	files, err := fvsrepo.StateFiles(cp.fvsLayerPath(digest), states[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[2].Path != "usr/share/value" || files[2].ContentDigest == "" {
		t.Fatalf("migrated files = %+v", files)
	}
}

func TestRemoveMissingLegacyLayerSkipsLegacyStore(t *testing.T) {
	cp := newTestCpak(t)
	legacyStore := filepath.Join(t.TempDir(), "legacy-store")
	if err := os.WriteFile(legacyStore, []byte("not a store"), 0o600); err != nil {
		t.Fatal(err)
	}
	cp.Options.DaBaDeeStoreOptions.Root = legacyStore
	if err := cp.removeLegacyLayer(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureFVSLayerPreservesUnixBackslashes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	cp := newTestCpak(t)
	digest := "systemd"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	name := `system-systemd\x2dmute\x2dconsole.slice`
	if err := os.WriteFile(filepath.Join(legacy, name), []byte("slice"), 0o644); err != nil {
		t.Fatal(err)
	}
	available, err := cp.ensureFVSLayer(digest)
	if err != nil || !available {
		t.Fatalf("available = %v, err = %v", available, err)
	}
	states, err := fvsrepo.States(cp.fvsLayerPath(digest))
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %d, err = %v", len(states), err)
	}
	files, err := fvsrepo.StateFiles(cp.fvsLayerPath(digest), states[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != name {
		t.Fatalf("migrated files = %+v", files)
	}
}
func TestEnsureFVSLayerMigratesUnreadableWhiteout(t *testing.T) {
	cp := newTestCpak(t)
	digest := "whiteout"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(filepath.Join(legacy, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "tmp", ".wh.removed")
	if err := os.WriteFile(marker, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0); err != nil {
		t.Fatal(err)
	}

	available, err := cp.ensureFVSLayer(digest)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("migrated layer is unavailable")
	}
	states, err := fvsrepo.States(cp.fvsLayerPath(digest))
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %d, err = %v", len(states), err)
	}
	files, err := fvsrepo.StateFiles(cp.fvsLayerPath(digest), states[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1].Path != "tmp/.wh.removed" || files[1].Mode != 0 || files[1].Size != 0 {
		t.Fatalf("migrated files = %+v", files)
	}
}

func TestEnsureFVSLayersReportsMigrationProgress(t *testing.T) {
	cp := newTestCpak(t)
	layers := []string{"first", "second"}
	for index, layer := range layers {
		legacy := cp.GetInStoreDir("layers", layer)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		content := bytes.Repeat([]byte{byte(index + 1)}, index+2)
		if err := os.WriteFile(filepath.Join(legacy, "data"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var reports []StorageMigrationProgress
	cp.SetStorageMigrationHandler(func(run func(func(StorageMigrationProgress)) error) error {
		return run(func(report StorageMigrationProgress) {
			reports = append(reports, report)
		})
	})
	if err := cp.ensureFVSLayers(layers); err != nil {
		t.Fatal(err)
	}
	if len(reports) < 4 {
		t.Fatalf("reports = %+v", reports)
	}
	last := reports[len(reports)-1]
	if last.Layer != 2 || last.Layers != 2 || last.Bytes != 5 || last.TotalBytes != 5 {
		t.Fatalf("last report = %+v", last)
	}
}

func TestEnsureMigrationSpaceRejectsOversizedLayer(t *testing.T) {
	err := ensureMigrationSpace(t.TempDir(), 1<<62)
	if err == nil {
		t.Fatal("oversized migration was accepted")
	}
}

func TestEnsureFVSLayerChecksSpaceBeforeMigration(t *testing.T) {
	cp := newTestCpak(t)
	digest := "oversized"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(legacy, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	var stats unix.Statfs_t
	if err := unix.Statfs(legacy, &stats); err != nil {
		file.Close()
		t.Fatal(err)
	}
	available := int64(stats.Bavail) * int64(stats.Bsize)
	if err := file.Truncate(available - storageMigrationReserve + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if available, err := cp.ensureFVSLayer(digest); err == nil || available {
		t.Fatalf("available = %v, err = %v", available, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy layer changed after rejected migration: %v", err)
	}
	if _, err := os.Stat(cp.fvsLayerPath(digest)); !os.IsNotExist(err) {
		t.Fatalf("FVS layer was published after rejected migration: %v", err)
	}
}

func TestEnsureFVSLayerResumesAfterMetadataPublish(t *testing.T) {
	cp := newTestCpak(t)
	digest := "published"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "value"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository, err := fvsrepo.InitWithOptions(cp.fvsLayerPath(digest), fvsrepo.InitOptions{BlocksPath: cp.fvsBlocksPath()})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.AddTree(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	available, err := cp.ensureFVSLayer(digest)
	if err != nil || !available {
		t.Fatalf("available = %v, err = %v", available, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy layer still present: %v", err)
	}
}

func TestAuditDoesNotMigrateLegacyLayers(t *testing.T) {
	cp := newTestCpak(t)
	digest := "legacy-audit"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "value"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedApplication(t, cp, types.Application{
		CpakId:       "legacy-app",
		Name:         "Legacy",
		Origin:       "github.com/containerpak/legacy",
		Branch:       "main",
		ParsedLayers: []string{digest},
	})
	if err := cp.Audit(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("audit changed the legacy layer: %v", err)
	}
	if _, err := os.Stat(cp.fvsLayerPath(digest)); !os.IsNotExist(err) {
		t.Fatalf("audit migrated the legacy layer: %v", err)
	}
}

func TestLayerLookupDoesNotMigrateLegacyLayers(t *testing.T) {
	cp := newTestCpak(t)
	digest := "legacy-lookup"
	legacy := cp.GetInStoreDir("layers", digest)
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "value"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	available, err := cp.layerAvailable(digest)
	if err != nil || !available {
		t.Fatalf("available = %v, err = %v", available, err)
	}
	if _, err := cp.fvsContentIndex(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("lookup changed the legacy layer: %v", err)
	}
	if _, err := os.Stat(cp.fvsLayerPath(digest)); !os.IsNotExist(err) {
		t.Fatalf("lookup migrated the legacy layer: %v", err)
	}
}

func TestRemoveApplicationLayersDoesNotAuditTheStore(t *testing.T) {
	cp := newTestCpak(t)
	removed := types.Application{ParsedLayers: []string{"unique", "shared"}}
	seedApplication(t, cp, types.Application{
		CpakId:       "remaining",
		Origin:       "github.com/containerpak/remaining",
		Branch:       "main",
		ParsedLayers: []string{"shared"},
	})
	for _, layer := range []string{"unique", "shared", "unrelated"} {
		if err := os.MkdirAll(cp.GetInStoreDir("layers", layer), 0o755); err != nil {
			t.Fatal(err)
		}
		seedFVSLayerFile(t, cp, layer, "value", []byte(layer))
	}
	if err := cp.removeApplicationLayers(removed); err != nil {
		t.Fatal(err)
	}
	for _, layer := range []string{"shared", "unrelated"} {
		for _, path := range []string{cp.GetInStoreDir("layers", layer), cp.fvsLayerPath(layer)} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("layer %s changed: %v", layer, err)
			}
		}
	}
	for _, path := range []string{cp.GetInStoreDir("layers", "unique"), cp.fvsLayerPath("unique")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unique layer remained: %s", path)
		}
	}
}

func TestPrepareLayerMountFallsBackWithoutTheStorageService(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CPAK_FVS2D_BINARY", "")
	cp := newTestCpak(t)
	layer := "legacy"
	if err := os.MkdirAll(cp.GetInStoreDir("layers", layer), 0o755); err != nil {
		t.Fatal(err)
	}
	mountID, mountPath, managerSocket, err := cp.prepareLayerMount(t.TempDir(), []string{layer})
	if err != nil {
		t.Fatal(err)
	}
	if mountID != "" || mountPath != "" {
		t.Fatalf("legacy fallback returned an FVS mount: %q %q", mountID, mountPath)
	}
	if managerSocket != "" {
		t.Fatalf("legacy fallback returned a manager socket: %q", managerSocket)
	}
	if _, err := os.Stat(cp.GetInStoreDir("layers", layer)); err != nil {
		t.Fatalf("legacy layer changed during fallback: %v", err)
	}
}

func TestPrepareLayerMountUsesPersistentNativeCheckouts(t *testing.T) {
	t.Setenv("CPAK_STORAGE_BACKEND", "native")
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "base", "base", []byte("base"))
	seedFVSLayerFile(t, cp, "top", "top", []byte("top"))
	app := types.Application{CpakId: "native-checkout", Origin: "github.com/containerpak/test", ParsedLayers: []string{"base", "top"}}
	if err := cp.PrepareApplicationStorage(app); err != nil {
		t.Fatal(err)
	}

	mountID, lowerDirs, managerSocket, err := cp.prepareLayerMount(t.TempDir(), app.ParsedLayers)
	if err != nil {
		t.Fatal(err)
	}
	if mountID != "" || managerSocket != "" {
		t.Fatalf("native checkouts returned a service lease: %q %q", mountID, managerSocket)
	}
	paths := strings.Split(lowerDirs, ":")
	if len(paths) != 2 {
		t.Fatalf("lower dirs = %q", lowerDirs)
	}
	if _, err := os.Stat(filepath.Join(paths[0], "top")); err != nil {
		t.Fatalf("highest-priority layer is not first: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths[1], "base")); err != nil {
		t.Fatalf("base layer is not last: %v", err)
	}
	if !nativeLayerCheckoutsAlive(lowerDirs) {
		t.Fatal("prepared checkout set is not reusable")
	}
}

func TestPrepareLayerMountReportsMissingCheckoutPreparation(t *testing.T) {
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "layer", "value", []byte("value"))
	shown := false
	cp.SetStoragePreparationHandler(func(run func() error) error {
		shown = true
		return run()
	})
	_, lowerDirs, _, err := cp.prepareLayerMount(t.TempDir(), []string{"layer"})
	if err != nil {
		t.Fatal(err)
	}
	if !shown {
		t.Fatal("storage preparation handler was not called")
	}
	if !nativeLayerCheckoutsAlive(lowerDirs) {
		t.Fatalf("prepared checkout is not reusable: %q", lowerDirs)
	}
}

func TestWithApplicationFilesystemFallsBackToLegacyLayers(t *testing.T) {
	requireRootlessOverlay(t)
	t.Setenv("CPAK_STORAGE_DRIVER_BINARY", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("CPAK_STORAGE_BINARY", "")
	t.Setenv("CPAK_FVS2D_BINARY", "")
	cp := newTestCpak(t)
	layers := []string{"base", "top"}
	for _, layer := range layers {
		path := cp.GetInStoreDir("layers", layer)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value"), []byte(layer), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var visited []string
	err := cp.WithApplicationFilesystem(types.Application{ParsedLayers: layers}, func(path string) error {
		content, err := os.ReadFile(filepath.Join(path, "value"))
		if err != nil {
			return err
		}
		visited = append(visited, string(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != 1 || visited[0] != "top" {
		t.Fatalf("visited = %v", visited)
	}
}
