package cpak

import (
	"os"
	"path/filepath"
	"strings"
)

// GetNvidiaLibs finds the paths of the libraries needed by the
// GPU drivers to run.
//
// Note: this follows the same logic as the one used in the
// distrobox utility to find the nvidia libraries, see:
// https://github.com/89luca89/distrobox/blob/9bea9498c58e367cea2f106492b5b5cbd8e6b713/distrobox-init#L1256
func GetNvidiaLibs() ([]string, error) {
	var nvidiaLibs []string
	directories := []string{
		"/etc",
		"/usr",
	}

	for _, directory := range directories {
		nvidiaLibs = append(nvidiaLibs, getNvidiaLibsFromDir(directory)...)
	}

	// Remove duplicates and hidden files.
	var cleanedNvidiaLibs []string
	for _, nvidiaLib := range nvidiaLibs {
		// if any of the components of the path starts with a dot, skip it.
		components := strings.Split(nvidiaLib, "/")
		isHidden := false
		for _, component := range components {
			if strings.HasPrefix(component, ".") {
				isHidden = true
				continue
			}
		}

		if isHidden {
			continue
		}

		// if any of the components of the path is already in the list,
		// skip it.
		var skip bool
		for _, cleanedNvidiaLib := range cleanedNvidiaLibs {
			if strings.HasPrefix(nvidiaLib, cleanedNvidiaLib) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		cleanedNvidiaLibs = append(cleanedNvidiaLibs, nvidiaLib)
	}

	return cleanedNvidiaLibs, nil
}

// getNvidiaLibsFromDir finds every file and directory in the given
// directory, which name contains the string "nvidia".
//
// Note: this is recursive, it calls itself for every directory
// found so it can find nvidia libraries in subdirectories.
func getNvidiaLibsFromDir(dir string) []string {
	var nvidiaLibs []string

	excludedDirs := []string{
		"/usr/src",
	}

	// Open the directory.
	directory, err := os.Open(dir)
	if err != nil {
		return nvidiaLibs
	}

	// Read the directory.
	files, err := directory.Readdir(0)
	if err != nil {
		return nvidiaLibs
	}

	// For every file in the directory.
	for _, file := range files {
		// If the file is in the excluded directories, skip it.
		var skip bool
		for _, excludedDir := range excludedDirs {
			if strings.HasPrefix(dir, excludedDir) {
				skip = true
				break
			}
		}

		// if one of the components of the path starts with a dot, skip it.
		components := strings.Split(dir, "/")
		for _, component := range components {
			if strings.HasPrefix(component, ".") {
				skip = true
				break
			}
		}

		if skip {
			continue
		}

		// If the file is a directory, call this function recursively.
		if file.IsDir() {
			nvidiaLibs = append(nvidiaLibs, getNvidiaLibsFromDir(filepath.Join(dir, file.Name()))...)
		}

		// If the file name contains the string "nvidia", add it to the list.
		if strings.Contains(file.Name(), "nvidia") {
			nvidiaLibs = append(nvidiaLibs, filepath.Join(dir, file.Name()))
		}
	}

	return nvidiaLibs
}
