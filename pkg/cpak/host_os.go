/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
)

const hostOSReleaseTarget = "/run/cpak/host-os-release"

func hostOSReleaseSource() string {
	candidates := []string{}
	if os.Getenv("CPAK_HOST_OS_RELEASE") == hostOSReleaseTarget {
		candidates = append(candidates, hostOSReleaseTarget)
	}
	if os.Getenv("container") != "" || fileExists("/.dockerenv") || fileExists("/run/.containerenv") {
		candidates = append(candidates, "/run/host/etc/os-release")
	}
	candidates = append(candidates, "/etc/os-release")
	return firstRegularFile(candidates...)
}

func firstRegularFile(paths ...string) string {
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err == nil && info.Mode().IsRegular() {
			return resolved
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
