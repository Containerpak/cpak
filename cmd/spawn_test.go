/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestWriteNvidiaLoaderConfigurationUsesSoname(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	destination := filepath.Join(root, "usr/share/vulkan/icd.d/nvidia.json")
	data := []byte(`{"ICD":{"library_path":"/usr/lib64/libGLX_nvidia.so.0"}}`)
	if err := os.WriteFile(source, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeNvidiaLoaderConfiguration(source, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ICD":{"library_path":"libGLX_nvidia.so.0"}}`
	if string(got) != want {
		t.Fatalf("rewritten loader config: got %s, want %s", got, want)
	}
}

func TestLayerDirectoriesUseOverlayPriority(t *testing.T) {
	got := layerDirectories("/layers", []string{"base", "top", "runtime"})
	want := []string{"/layers/runtime", "/layers/top", "/layers/base"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected overlay order: got %v, want %v", got, want)
	}
}

func TestPrepareRootfsBindTargetReusesAnExistingMount(t *testing.T) {
	rootfs := t.TempDir()
	source := filepath.Join(t.TempDir(), "service.sock")
	if err := os.WriteFile(source, []byte("socket"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(rootfs, "run/cpak/service.sock")
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, destination); err != nil {
		t.Fatal(err)
	}
	actual, needsMount, err := prepareRootfsBindTarget(rootfs, "/run/cpak/service.sock", source)
	if err != nil {
		t.Fatal(err)
	}
	if actual != destination || needsMount {
		t.Fatalf("existing mount: path %s, needs mount %t", actual, needsMount)
	}
}

func TestSetEnvironmentVariablesIdentifiesContainer(t *testing.T) {
	env := setEnvironmentVariables("container-id", "/rootfs", []string{"LANG=C"}, "/state", "/layers", "base|")
	want := []string{
		"LANG=C",
		"CPAK_CONTAINER_ID=container-id",
		"CPAK_ROOTFS=/rootfs",
		"CPAK_STATE_DIR=/state",
		"CPAK_LAYERS_DIR=/layers",
		"CPAK_LAYERS=base|",
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("environment: got %v, want %v", env, want)
	}
}

func TestGenerateMachineIDUsesAPrivateContainerIdentifier(t *testing.T) {
	machineID, err := generateMachineID(bytes.NewReader([]byte("0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}
	if machineID != "30313233343536373839616263646566" {
		t.Fatalf("machine ID: %s", machineID)
	}
}

func TestCreateSystemBrokerShimIsExecutable(t *testing.T) {
	rootfs := t.TempDir()
	command := &SpawnCmd{}
	if err := command.createSystemBrokerShimAndLinks(rootfs, []string{"notify-send", "xdg-open"}); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(rootfs, systemBrokerShimPath)
	info, err := os.Stat(shim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("shim mode: got %o, want 755", info.Mode().Perm())
	}
	for _, name := range []string{"notify-send", "xdg-open"} {
		link := filepath.Join(rootfs, "usr/local/bin", name)
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("system broker link %s: %v", name, err)
		}
	}
}

func TestRuntimePackageCommandUsesGuestDpkg(t *testing.T) {
	command := runtimePackageCommand([]string{"/run/cpak/runtime/demo.deb"})
	if command.Path != "/usr/bin/dpkg" {
		t.Fatalf("runtime installer path: %s", command.Path)
	}
	wantArgs := []string{"/usr/bin/dpkg", "--install", "/run/cpak/runtime/demo.deb"}
	if !reflect.DeepEqual(command.Args, wantArgs) {
		t.Fatalf("runtime installer arguments: %v", command.Args)
	}
	if !slices.Contains(command.Env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("guest PATH is missing: %v", command.Env)
	}
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "CPAK_") {
			t.Fatalf("host cpak variable leaked into runtime installer: %s", entry)
		}
	}
}

func TestDecodeFilesystemPermissionsRejectsDuplicates(t *testing.T) {
	permission := types.FilesystemPermission{Path: "/home/user", Access: "read-write"}
	encoded, err := types.EncodeFilesystemPermission(permission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeFilesystemPermissions([]string{encoded, encoded}); err == nil {
		t.Fatal("accepted duplicate filesystem permission")
	}
}

func TestDecodeFilesystemPermissionsKeepsPortableScope(t *testing.T) {
	encoded, err := types.EncodeFilesystemPermission(types.FilesystemPermission{Path: "host", Access: "read-only"})
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := decodeFilesystemPermissions([]string{encoded})
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 1 || permissions[0] != (types.FilesystemPermission{Path: "host", Access: "read-only"}) {
		t.Fatalf("got %v", permissions)
	}
}

func TestResolveOverrideMountSourceUsesHostRootFallback(t *testing.T) {
	hostRoot := t.TempDir()
	target := filepath.Join(t.TempDir(), "run/dbus/system_bus_socket")
	source := filepath.Join(hostRoot, strings.TrimPrefix(target, "/"))
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("socket"), 0600); err != nil {
		t.Fatal(err)
	}
	got, found, err := resolveOverrideMountSource(target, hostRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != source {
		t.Fatalf("override source: got %q, found %t, want %q", got, found, source)
	}
}
