/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMatchNonLibConfigRecognizesVersionedVulkanFiles(t *testing.T) {
	for _, path := range []string{
		"/usr/share/vulkan/icd.d/nvidia_icd.x86_64.json",
		"/usr/share/vulkan/icd.d/nvidia_layers.i686.json",
		"/usr/share/vulkan/implicit_layer.d/nvidia_layers.json",
	} {
		if !matchNonLibConfig(path, nil) {
			t.Fatalf("matchNonLibConfig(%q) returned false", path)
		}
	}
}

func TestNvidiaGuestLibraryRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr/lib/x86_64-linux-gnu"), 0755); err != nil {
		t.Fatal(err)
	}
	lib64, lib32 := nvidiaGuestLibraryRoots(root)
	if lib64 != "/usr/lib/x86_64-linux-gnu" || lib32 != "/usr/lib/i386-linux-gnu" {
		t.Fatalf("library roots: got %q and %q", lib64, lib32)
	}
}

func TestNvidiaPluginTailKeepsFixedDirectory(t *testing.T) {
	cases := map[string]string{
		"/usr/lib64/nvidia/wine/nvngx.dll":                   "nvidia/wine/nvngx.dll",
		"/usr/lib/x86_64-linux-gnu/gbm/nvidia-drm_gbm.so":    "gbm/nvidia-drm_gbm.so",
		"/usr/lib/i386-linux-gnu/vdpau/libvdpau_nvidia.so.1": "vdpau/libvdpau_nvidia.so.1",
	}
	for input, want := range cases {
		if got := nvidiaPluginTail(input); got != want {
			t.Fatalf("plugin tail for %s: got %s, want %s", input, got, want)
		}
	}
}

func TestELFClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.so")
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F', 1}, 0644); err != nil {
		t.Fatal(err)
	}
	if got := elfClass(path); got != 1 {
		t.Fatalf("ELF class: got %d, want 1", got)
	}
}

func TestNvidiaMountsPreserveSonameAliasesAndELFClasses(t *testing.T) {
	rootFs := filepath.Join(t.TempDir(), "rootfs")
	if err := os.MkdirAll(filepath.Join(rootFs, "usr/lib/x86_64-linux-gnu"), 0755); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(t.TempDir(), "host")
	if err := os.MkdirAll(host, 0755); err != nil {
		t.Fatal(err)
	}
	real64 := filepath.Join(host, "libnvidia-real64.so")
	real32 := filepath.Join(host, "libnvidia-real32.so")
	if err := os.WriteFile(real64, []byte{0x7f, 'E', 'L', 'F', 2}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real32, []byte{0x7f, 'E', 'L', 'F', 1}, 0644); err != nil {
		t.Fatal(err)
	}
	soname64 := filepath.Join(host, "libcuda.so.1")
	soname32 := filepath.Join(host, "libcuda.so.1.32")
	if err := os.Symlink(real64, soname64); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real32, soname32); err != nil {
		t.Fatal(err)
	}
	mounts := nvidiaMountsForFiles(rootFs, []string{soname64, soname32})
	want := []NvidiaMount{
		{Source: real64, Destination: "/usr/lib/cpak-nvidia/lib64/libcuda.so.1"},
		{Source: real32, Destination: "/usr/lib/cpak-nvidia/lib32/libcuda.so.1.32"},
	}
	if !reflect.DeepEqual(mounts, want) {
		t.Fatalf("NVIDIA mounts: got %#v, want %#v", mounts, want)
	}
}

func TestGetNvidiaLibraryDirs(t *testing.T) {
	files := []string{
		"/usr/lib64/libnvidia-glsi.so.565.77",
		"/usr/lib64/libcuda.so.1",
		"/usr/lib/libnvidia-eglcore.so.565.77",
		"/usr/share/vulkan/icd.d/nvidia_icd.json",
		"/usr/bin/nvidia-smi",
	}

	want := []string{"/usr/lib", "/usr/lib64"}
	if got := GetNvidiaLibraryDirs(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetNvidiaLibraryDirs() = %v, want %v", got, want)
	}
}
