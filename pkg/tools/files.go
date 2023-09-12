package tools

import "path/filepath"

// ResolvePath resolves the given path, following symlinks.
// It is just a wrapper around filepath.EvalSymlinks, for convenience.
func ResolvePath(path string) string {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return realPath
}
