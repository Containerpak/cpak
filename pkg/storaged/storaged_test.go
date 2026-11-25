package storaged

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerpak/storage/pkg/conformance"
	storage "github.com/containerpak/storage/pkg/driver"
	fvsrepo "github.com/fvs-lab/fvs2/repo"
)

func TestFVSConformance(t *testing.T) {
	runConformance(t, "fvs")
}

func TestDaBaDeeConformance(t *testing.T) {
	runConformance(t, "dabadee")
}

func TestFVSVerifyRepairsChangedContent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	driverRoot := filepath.Join(root, "driver")
	seedLayer(t, source, "layer")
	handler, err := NewFVS(source, driverRoot)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := handler.Prepare(context.Background(), storage.PrepareRequest{
		Layers: []string{"layer"}, ClearPrivilegedBits: true,
		OverlayWhiteouts: true, PruneLooseBlocks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(prepared.LowerDirs[0], "usr/share/layer")
	replacement := filepath.Join(prepared.LowerDirs[0], "usr/share/.replacement")
	if err := os.WriteFile(replacement, []byte("broken content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, file); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Verify(context.Background(), storage.VerifyRequest{Layers: []string{"layer"}}); err == nil {
		t.Fatal("changed checkout passed verification")
	}
	result, err := handler.Verify(context.Background(), storage.VerifyRequest{Layers: []string{"layer"}, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verified != 1 || result.Repaired != 1 {
		t.Fatalf("verify result = %+v", result)
	}
}

func TestDriverGarbageCollectsStalePartialCheckouts(t *testing.T) {
	root := t.TempDir()
	layers := filepath.Join(root, "layers")
	if err := os.MkdirAll(layers, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(layers, ".stale.partial-1")
	fresh := filepath.Join(layers, ".fresh.partial-2")
	live := filepath.Join(layers, "live")
	for _, path := range []string{stale, fresh, live} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-partialCheckoutGrace - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	preview, err := collectDriverGarbage(context.Background(), root, []string{"live"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Layers) != 1 || preview.Layers[0] != ".stale.partial-1" || preview.Bytes != 4 {
		t.Fatalf("preview = %+v", preview)
	}

	applied, err := collectDriverGarbage(context.Background(), root, []string{"live"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Layers) != 1 || applied.Layers[0] != ".stale.partial-1" {
		t.Fatalf("applied = %+v", applied)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale partial still exists: %v", err)
	}
	for _, path := range []string{fresh, live} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kept checkout %s: %v", path, err)
		}
	}
}

func runConformance(t *testing.T, name string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	driverRoot := filepath.Join(root, "driver")
	var handler storage.Handler
	var err error
	if name == "fvs" {
		handler, err = NewFVS(source, driverRoot)
	} else {
		handler, err = NewDaBaDee(source, driverRoot)
	}
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, conformance.Harness{
		New:  func(*testing.T) storage.Handler { return handler },
		Root: func(*testing.T) string { return driverRoot },
		Seed: func(t *testing.T, layer string) { seedLayer(t, source, layer) },
	})
}

func seedLayer(t *testing.T, sourceRoot, layer string) {
	t.Helper()
	repository, err := fvsrepo.InitWithOptions(filepath.Join(sourceRoot, "layers", layer), fvsrepo.InitOptions{
		BlocksPath: filepath.Join(sourceRoot, "blocks"),
	})
	if err != nil {
		t.Fatal(err)
	}
	states, err := fvsrepo.States(repository.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) > 0 {
		return
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("shared content")
	if err := writer.Add(fvsrepo.Entry{Path: "usr/share/" + layer, Kind: fvsrepo.EntryFile, Mode: 0o644, Size: int64(len(content))}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFVSRemovesCheckoutsWithReadOnlyDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	for _, method := range []string{"remove", "gc"} {
		t.Run(method, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			driverRoot := filepath.Join(root, "driver")
			seedReadOnlyLayer(t, source, "layer")
			handler, err := NewFVS(source, driverRoot)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := handler.Prepare(context.Background(), storage.PrepareRequest{
				Layers: []string{"layer"}, ClearPrivilegedBits: true,
				OverlayWhiteouts: true, PruneLooseBlocks: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(prepared.LowerDirs[0], "usr/bin"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o555 {
				t.Fatalf("checkout did not keep the image directory mode: %v", info.Mode().Perm())
			}
			if method == "gc" {
				_, err = handler.GC(context.Background(), storage.GCRequest{Apply: true})
			} else {
				_, err = handler.Remove(context.Background(), storage.RemoveRequest{Layers: []string{"layer"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(driverRoot, "layers", "layer")); !os.IsNotExist(err) {
				t.Fatalf("checkout still exists: %v", err)
			}
		})
	}
}

func seedReadOnlyLayer(t *testing.T, sourceRoot, layer string) {
	t.Helper()
	repository, err := fvsrepo.InitWithOptions(filepath.Join(sourceRoot, "layers", layer), fvsrepo.InitOptions{
		BlocksPath: filepath.Join(sourceRoot, "blocks"),
	})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"usr", "usr/bin"} {
		if err := writer.Add(fvsrepo.Entry{Path: directory, Kind: fvsrepo.EntryDir, Mode: 0o555}, nil); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("binary")
	if err := writer.Add(fvsrepo.Entry{Path: "usr/bin/WebKitWebDriver", Kind: fvsrepo.EntryFile, Mode: 0o755, Size: int64(len(content))}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
}
