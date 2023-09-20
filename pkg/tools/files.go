package tools

import (
	"os"
	"path/filepath"
)

// ResolvePath resolves the given path, following symlinks.
// It is just a wrapper around filepath.EvalSymlinks, for convenience.
func ResolvePath(path string) string {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return realPath
}

// IsSymlink checks if the given path is a symlink.
// It is just a wrapper around os.Lstat, for convenience.
func IsSymlink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}
