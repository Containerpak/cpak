/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	nvidiaLibraryRoot = "/usr/lib/cpak-nvidia"
	nvidiaLib64       = nvidiaLibraryRoot + "/lib64"
	nvidiaLib32       = nvidiaLibraryRoot + "/lib32"
)

type NvidiaMount struct {
	Source             string
	Destination        string
	RewriteLibraryPath bool
}

// GetNvidiaLibs returns all files relevant for NVIDIA integration.
// This includes configuration files, binaries, and libraries.
// Highly inspired to the Distrobox NVIDIA implementation.
func GetNvidiaLibs() ([]string, error) {
	var files []string
	specs := []struct {
		base      string
		predicate func(string, fs.DirEntry) bool
	}{
		{"/etc", matchGenericConfig},
		{"/usr", matchNonLibConfig},
		{"/bin", matchBinary},
		{"/sbin", matchBinary},
		{"/usr/bin", matchBinary},
		{"/usr/sbin", matchBinary},
		{"/usr/lib", matchLibrary},
		{"/usr/lib64", matchLibrary},
		{"/usr/lib32", matchLibrary},
	}

	for _, spec := range specs {
		if info, err := os.Stat(spec.base); err != nil || !info.IsDir() {
			continue
		}
		res, err := walkAndFilter(spec.base, spec.predicate)
		if err != nil {
			continue
		}
		files = append(files, res...)
	}

	// Remove duplicates and files with hidden components.
	cleaned := []string{}
	seen := make(map[string]struct{})
	for _, f := range files {
		if isHiddenPath(f) {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		cleaned = append(cleaned, f)
	}

	return cleaned, nil
}

func GetNvidiaMounts(rootFs string) ([]NvidiaMount, error) {
	files, err := GetNvidiaLibs()
	if err != nil {
		return nil, err
	}
	return nvidiaMountsForFiles(rootFs, files), nil
}

func nvidiaMountsForFiles(rootFs string, files []string) []NvidiaMount {
	lib64Root, lib32Root := nvidiaGuestLibraryRoots(rootFs)
	mounts := make([]NvidiaMount, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, source := range files {
		originalSource := source
		destination := source
		rewrite := false
		if isNvidiaLibraryPath(source) {
			resolved, resolveErr := filepath.EvalSymlinks(source)
			if resolveErr == nil {
				source = resolved
			}
			class := elfClass(source)
			if class == 0 {
				class = inferredELFClass(originalSource)
			}
			if isFixedPathNvidiaPlugin(originalSource) {
				root := lib64Root
				if class == 1 {
					root = lib32Root
				}
				if root == "" {
					continue
				}
				destination = filepath.Join(root, nvidiaPluginTail(originalSource))
			} else if class == 1 {
				destination = filepath.Join(nvidiaLib32, filepath.Base(originalSource))
			} else {
				destination = filepath.Join(nvidiaLib64, filepath.Base(originalSource))
			}
		} else if strings.HasSuffix(strings.ToLower(source), ".json") && isNvidiaLoaderConfiguration(source) {
			rewrite = true
		}
		if seen[destination] {
			continue
		}
		seen[destination] = true
		mounts = append(mounts, NvidiaMount{
			Source:             source,
			Destination:        destination,
			RewriteLibraryPath: rewrite,
		})
	}
	return mounts
}

func NvidiaLibraryDirs() []string {
	return []string{nvidiaLib64, nvidiaLib32}
}

func nvidiaGuestLibraryRoots(rootFs string) (lib64, lib32 string) {
	for _, candidate := range []string{"/usr/lib/x86_64-linux-gnu", "/usr/lib64", "/usr/lib"} {
		if info, err := os.Stat(filepath.Join(rootFs, candidate)); err == nil && info.IsDir() {
			lib64 = candidate
			break
		}
	}
	if lib64 == "" {
		lib64 = "/usr/lib"
	}
	switch {
	case strings.HasSuffix(lib64, "/x86_64-linux-gnu"):
		lib32 = "/usr/lib/i386-linux-gnu"
	case strings.HasSuffix(lib64, "/lib64"):
		if sameResolvedPath(filepath.Join(rootFs, "/usr/lib64"), filepath.Join(rootFs, "/usr/lib")) {
			lib32 = "/usr/lib32"
		} else {
			lib32 = "/usr/lib"
		}
	default:
		lib32 = "/usr/lib32"
	}
	if sameResolvedPath(filepath.Join(rootFs, lib64), filepath.Join(rootFs, lib32)) {
		lib32 = ""
	}
	return lib64, lib32
}

func sameResolvedPath(first, second string) bool {
	firstPath, firstErr := filepath.EvalSymlinks(first)
	secondPath, secondErr := filepath.EvalSymlinks(second)
	return firstErr == nil && secondErr == nil && firstPath == secondPath
}

func elfClass(path string) byte {
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()
	header := make([]byte, 5)
	if _, err := file.Read(header); err != nil {
		return 0
	}
	if binary.BigEndian.Uint32(header[:4]) != 0x7f454c46 {
		return 0
	}
	if header[4] == 1 || header[4] == 2 {
		return header[4]
	}
	return 0
}

func inferredELFClass(path string) byte {
	lower := strings.ToLower(path)
	if strings.Contains(lower, "/lib32/") || strings.Contains(lower, "/i386-linux-gnu/") || strings.Contains(lower, "/i686-linux-gnu/") {
		return 1
	}
	return 2
}

func isNvidiaLibraryPath(path string) bool {
	return matchLibraryName(filepath.Base(path)) || strings.Contains(strings.ToLower(path), "/nvidia/wine/")
}

func isFixedPathNvidiaPlugin(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{"/xorg/", "/gbm/", "/vdpau/", "/nvidia/wine/"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func nvidiaPluginTail(path string) string {
	cleaned := filepath.Clean(path)
	parts := strings.Split(strings.TrimPrefix(cleaned, "/usr/lib/"), "/")
	if len(parts) > 1 && strings.Contains(parts[0], "linux") {
		parts = parts[1:]
	} else if strings.HasPrefix(cleaned, "/usr/lib64/") {
		parts = strings.Split(strings.TrimPrefix(cleaned, "/usr/lib64/"), "/")
	} else if strings.HasPrefix(cleaned, "/usr/lib32/") {
		parts = strings.Split(strings.TrimPrefix(cleaned, "/usr/lib32/"), "/")
	}
	return filepath.Join(parts...)
}

func isNvidiaLoaderConfiguration(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "/vulkan/") ||
		strings.Contains(lower, "/vulkansc/") ||
		strings.Contains(lower, "/glvnd/") ||
		strings.Contains(lower, "/egl_external_platform.d/")
}

func GetNvidiaLibraryDirs(files []string) []string {
	dirs := make(map[string]struct{})
	for _, file := range files {
		if !matchLibraryName(filepath.Base(file)) {
			continue
		}
		dirs[filepath.Dir(file)] = struct{}{}
	}

	result := make([]string, 0, len(dirs))
	for dir := range dirs {
		result = append(result, dir)
	}
	sort.Strings(result)
	return result
}

// walkAndFilter walks through the directory tree rooted at 'root'
// and returns files that satisfy the predicate.
func walkAndFilter(root string, predicate func(string, fs.DirEntry) bool) ([]string, error) {
	var results []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if predicate(path, d) {
			results = append(results, path)
		}
		return nil
	})
	return results, err
}

// matchGenericConfig returns true if the file path contains "nvidia".
// Used to match generic configuration files in /etc.
func matchGenericConfig(path string, d fs.DirEntry) bool {
	return strings.Contains(path, "nvidia")
}

// group2Patterns holds patterns for non-library configuration files in /usr.
var group2Patterns = []string{
	"glvnd/egl_vendor.d/10_nvidia.json",
	"OpenCL/vendors/nvidia",
	"X11/xorg.conf.d/10-nvidia.conf",
	"X11/xorg.conf.d/nvidia-drm-outputclass.conf",
	"egl/egl_external_platform.d/10_nvidia_wayland.json",
	"egl/egl_external_platform.d/15_nvidia_gbm.json",
	"nvidia/nvoptix.bin",
	"share/nvidia/application-profiles",
	"vulkan/explicit_layer.d/nvidia",
	"vulkan/icd.d/nvidia_icd",
	"vulkan/icd.d/nvidia_layers",
	"vulkan/implicit_layer.d/nvidia_layers",
	"vulkansc/icd.d/nvidia",
	"nvidia.icd",
	"nvidia.yaml",
	"nvidia.json",
}

// matchNonLibConfig returns true if the file path contains any of the patterns
// specified in group2Patterns.
func matchNonLibConfig(path string, d fs.DirEntry) bool {
	for _, pat := range group2Patterns {
		if strings.Contains(path, pat) {
			return true
		}
	}
	return false
}

// matchBinary returns true if the file name (lowercased) contains "nvidia".
// Used to match Nvidia CLI utilities.
func matchBinary(path string, d fs.DirEntry) bool {
	return strings.Contains(strings.ToLower(d.Name()), "nvidia")
}

// matchLibrary returns true if the file is a library matching Nvidia or CUDA patterns.
// It checks for specific prefixes and substring conditions in the file name.
func matchLibrary(path string, d fs.DirEntry) bool {
	return matchLibraryName(d.Name()) || strings.Contains(strings.ToLower(path), "/nvidia/wine/")
}

func matchLibraryName(name string) bool {
	if strings.HasPrefix(name, "libnvcuvid") || strings.HasPrefix(name, "libnvoptix") {
		return true
	}
	if strings.Contains(strings.ToLower(name), ".so") {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "nvidia") || strings.Contains(lower, "cuda") {
			return true
		}
	}
	return false
}

// isHiddenPath returns true if any component of the path starts with a dot.
func isHiddenPath(path string) bool {
	parts := strings.Split(path, string(os.PathSeparator))
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
