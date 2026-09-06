package cpak

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestStorageDriverProtocolPreparesRuntimeIndex(t *testing.T) {
	binary := os.Getenv("CPAK_STORAGE_DRIVER_TEST_BINARY")
	if binary == "" {
		t.Skip("CPAK_STORAGE_DRIVER_TEST_BINARY is not set")
	}
	t.Setenv("CPAK_STORAGE_DRIVER_BINARY", binary)
	t.Setenv("CPAK_STORAGE_DRIVER_TRUSTED", "1")
	t.Setenv("CPAK_STORAGE_DRIVER", "fvs")
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, "base", "base", []byte("base"))
	seedFVSLayerFile(t, cp, "top", "top", []byte("top"))
	app := types.Application{
		CpakId: "driver-protocol", Origin: "github.com/containerpak/test", ParsedLayers: []string{"base", "top"},
	}
	if err := cp.PrepareApplicationStorage(app); err != nil {
		t.Fatal(err)
	}
	paths, err := cp.preparedLayerDirectories(app.ParsedLayers)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("prepared paths = %v", paths)
	}
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "checkout.json")); err != nil {
			t.Fatal(err)
		}
	}
	socket, err := cp.storageDriverSocket("fvs")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("storage driver remained active after preparation")
}

func TestInstalledStorageMigrationStatusAndRepair(t *testing.T) {
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "layer", "usr/share/value", []byte("value"))
	app := types.Application{
		CpakId: "storage-migration", Origin: "github.com/containerpak/test", ParsedLayers: []string{"layer"},
	}
	seedApplication(t, cp, app)
	status, err := cp.StorageStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Layers != 1 || status.Prepared != 0 || status.Missing != 1 {
		t.Fatalf("initial status = %+v", status)
	}
	status, err = cp.PrepareInstalledStorage()
	if err != nil {
		t.Fatal(err)
	}
	if status.Prepared != 1 || status.Missing != 0 {
		t.Fatalf("migrated status = %+v", status)
	}
	paths, err := cp.preparedLayerDirectories(app.ParsedLayers)
	if err != nil {
		t.Fatal(err)
	}
	value := filepath.Join(paths[0], "usr/share/value")
	replacement := filepath.Join(paths[0], "usr/share/.replacement")
	if err := os.WriteFile(replacement, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, value); err != nil {
		t.Fatal(err)
	}
	status, err = cp.PrepareInstalledStorage()
	if err != nil {
		t.Fatalf("migration revisited a prepared layer: %v", err)
	}
	if status.Prepared != 1 || status.Missing != 0 {
		t.Fatalf("status after repeated migration = %+v", status)
	}
	if _, err := cp.VerifyPreparedStorage(false); err == nil {
		t.Fatal("changed checkout passed verification")
	}
	verified, err := cp.VerifyPreparedStorage(true)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Verified != 1 || verified.Repaired != 1 {
		t.Fatalf("verify result = %+v", verified)
	}
}

func TestExternalStorageDriverCommandIsSandboxed(t *testing.T) {
	cp := newTestCpak(t)
	if err := os.MkdirAll(cp.fvsRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cp.storageDriverRoot("external"), 0o700); err != nil {
		t.Fatal(err)
	}
	socket, err := cp.storageDriverSocket("external")
	if err != nil {
		t.Fatal(err)
	}
	command, err := cp.storageDriverCommand("external", "/bin/true", true, socket, []string{"--probe"})
	if err != nil {
		t.Fatal(err)
	}
	if command.SysProcAttr == nil || command.SysProcAttr.Cloneflags&syscall.CLONE_NEWNET == 0 {
		t.Fatal("external driver does not use a private network namespace")
	}
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{"launch", "--require-sandbox", "--landlock-read-write", cp.fvsRoot(), "--landlock-read-write", cp.storageDriverRoot("external"), "-- /bin/true --probe"} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("external driver command %q does not contain %q", arguments, expected)
		}
	}
	if strings.Contains(arguments, "--landlock-read-only "+cp.fvsRoot()) {
		t.Fatalf("external driver cannot lock its source repositories: %q", arguments)
	}
}

func TestStorageDriverFoundOnPathIsExternal(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "cpak-storaged")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("CPAK_STORAGE_DRIVER_BINARY", "")
	found, external, err := findStorageDriverService()
	if err != nil {
		t.Fatal(err)
	}
	if found != binary || !external {
		t.Fatalf("driver = %q, external = %v", found, external)
	}
}

func TestDaBaDeeStorageMigration(t *testing.T) {
	cp := newTestCpak(t)
	cp.Options.StorageDriver = "dabadee"
	handler, err := storaged.NewDaBaDee(cp.fvsRoot(), cp.storageDriverRoot("dabadee"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, cp, "layer", "usr/share/value", []byte("value"))
	app := types.Application{
		CpakId: "dabadee-migration", Origin: "github.com/containerpak/test", ParsedLayers: []string{"layer"},
	}
	seedApplication(t, cp, app)
	status, err := cp.PrepareInstalledStorage()
	if err != nil {
		t.Fatal(err)
	}
	if status.Driver != "dabadee" || status.Prepared != 1 || status.Missing != 0 {
		t.Fatalf("status = %+v", status)
	}
	if _, err := os.Stat(filepath.Join(cp.storageDriverRoot("dabadee"), "layers", "layer", ".dabadee-ready")); err != nil {
		t.Fatal(err)
	}
	verified, err := cp.VerifyPreparedStorage(false)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Verified != 1 {
		t.Fatalf("verify result = %+v", verified)
	}
	paths, err := cp.preparedLayerDirectories(app.ParsedLayers)
	if err != nil {
		t.Fatal(err)
	}
	value := filepath.Join(paths[0], "usr/share/value")
	damaged := value + ".damaged"
	if err := os.WriteFile(damaged, []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(damaged, value); err != nil {
		t.Fatal(err)
	}
	verified, err = cp.VerifyPreparedStorage(true)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Verified != 1 || verified.Repaired != 1 {
		t.Fatalf("repair result = %+v", verified)
	}
}

func TestPreparedLayersMountWithRootlessOverlay(t *testing.T) {
	mount, err := exec.LookPath("mount")
	if err != nil {
		t.Skip("mount is not installed")
	}
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedOverlayLayer(t, cp, "base", false, "base")
	seedOverlayLayer(t, cp, "top", true, "top")
	layers := []string{"base", "top"}
	if _, err := cp.prepareStorageDriver(layers); err != nil {
		t.Fatal(err)
	}
	lowerDirs, err := cp.preparedLayerDirectories(layers)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"upper", "work", "merged"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	options := strings.Join([]string{
		"lowerdir=" + strings.Join(lowerDirs, ":"),
		"upperdir=" + filepath.Join(root, "upper"),
		"workdir=" + filepath.Join(root, "work"),
		"userxattr",
	}, ",")
	script := `mount -t overlay overlay -o "$1" "$2" && test ! -e "$2/app/removed" && test "$(cat "$2/app/value")" = top`
	command := nativeNamespaceCommand("/bin/sh", []string{"-c", script, "sh", options, filepath.Join(root, "merged")}, namespaceOptions{})
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("rootless overlay is unavailable: %v: %s; mount=%s", err, output, mount)
	}
}

func TestWithApplicationFilesystemUsesPreparedOverlay(t *testing.T) {
	requireRootlessOverlay(t)
	cp := newTestCpak(t)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedOverlayLayer(t, cp, "base", false, "base")
	seedOverlayLayer(t, cp, "top", true, "top")
	app := types.Application{CpakId: "overlay-view", Origin: "github.com/containerpak/test", ParsedLayers: []string{"base", "top"}}
	if err := cp.WithApplicationFilesystem(app, func(root string) error {
		if _, err := os.Stat(filepath.Join(root, "app", "removed")); !os.IsNotExist(err) {
			return fmt.Errorf("whiteout target exists: %v", err)
		}
		content, err := os.ReadFile(filepath.Join(root, "app", "value"))
		if err != nil {
			return err
		}
		if string(content) != "top" {
			return fmt.Errorf("value = %q", content)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeOverlayViewMountFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := withNativeOverlayView([]string{missing}, func(string) error {
		t.Fatal("callback called for an invalid lower directory")
		return nil
	}); err == nil {
		t.Fatal("invalid lower directory mounted")
	}
}

func requireRootlessOverlay(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"lower", "upper", "work", "merged"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	options := strings.Join([]string{
		"lowerdir=" + filepath.Join(root, "lower"),
		"upperdir=" + filepath.Join(root, "upper"),
		"workdir=" + filepath.Join(root, "work"),
		"userxattr",
	}, ",")
	script := `mount -t overlay overlay -o "$1" "$2" && umount "$2"`
	command := nativeNamespaceCommand("/bin/sh", []string{"-c", script, "sh", options, filepath.Join(root, "merged")}, namespaceOptions{})
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("rootless overlay is unavailable: %v: %s", err, output)
	}
}

func seedOverlayLayer(t *testing.T, cp *Cpak, layer string, whiteout bool, value string) {
	t.Helper()
	repository, err := fvsrepo.InitWithOptions(cp.fvsLayerPath(layer), fvsrepo.InitOptions{BlocksPath: cp.fvsBlocksPath()})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := fvsrepo.BeginSnapshot(repository.Path, fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Add(fvsrepo.Entry{Path: "app", Kind: fvsrepo.EntryDir, Mode: 0o755}, nil); err != nil {
		t.Fatal(err)
	}
	if whiteout {
		if err := writer.Add(fvsrepo.Entry{Path: "app/.wh.removed", Kind: fvsrepo.EntryFile, Mode: 0o644}, bytes.NewReader(nil)); err != nil {
			t.Fatal(err)
		}
	} else {
		content := []byte("removed")
		if err := writer.Add(fvsrepo.Entry{Path: "app/removed", Kind: fvsrepo.EntryFile, Mode: 0o644, Size: int64(len(content))}, bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte(value)
	if err := writer.Add(fvsrepo.Entry{Path: "app/value", Kind: fvsrepo.EntryFile, Mode: 0o644, Size: int64(len(content))}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkPreparedLayerDirectories(b *testing.B) {
	cp := newTestCpak(b)
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		b.Fatal(err)
	}
	cp.storageDriver = handler
	layers := make([]string, 0, 10)
	for index := range 10 {
		layer := fmt.Sprintf("layer-%d", index)
		layers = append(layers, layer)
		seedFVSLayerFile(b, cp, layer, "value", []byte(layer))
	}
	if _, err := cp.prepareStorageDriver(layers); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := cp.preparedLayerDirectories(layers); err != nil {
			b.Fatal(err)
		}
	}
}
