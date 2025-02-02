package cpak

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

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
	"X11/xorg.conf.d/10-nvidia.conf",
	"X11/xorg.conf.d/nvidia-drm-outputclass.conf",
	"egl/egl_external_platform.d/10_nvidia_wayland.json",
	"egl/egl_external_platform.d/15_nvidia_gbm.json",
	"nvidia/nvoptix.bin",
	"vulkan/icd.d/nvidia_icd.json",
	"vulkan/icd.d/nvidia_layers.json",
	"vulkan/implicit_layer.d/nvidia_layers.json",
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
	name := d.Name()
	if strings.HasPrefix(name, "libnvcuvid") || strings.HasPrefix(name, "libnvoptix") {
		return true
	}
	if strings.Contains(name, ".so") {
		if strings.Contains(name, "nvidia") || strings.Contains(name, "cuda") {
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
