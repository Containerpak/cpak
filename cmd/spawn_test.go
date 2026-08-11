/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
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
