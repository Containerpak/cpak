package tools

import (
	"errors"
	"io"
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

// CopyFile copies the file at the given source path to the given
// destination path.
func CopyFile(src, dest string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return nil
}

// CopyDirContent copies the content of the directory at the given source path
// to the given destination path.
//
// Note: this function should NOT BE USED to copy the directory content to
// another directory since it ignores symlinks! This is ONLY USED while
// exporting desktop entries icons and binaries.
func CopyDirContent(src, dest string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if !srcInfo.IsDir() {
		return errors.New("source is not a directory")
	}

	if err := os.MkdirAll(dest, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		destPath := filepath.Join(dest, entry.Name())

		if entry.IsDir() {
			if err := CopyDirContent(srcPath, destPath); err != nil {
				return err
			}
		} else if entry.Type() == os.ModeSymlink {
			continue
		} else {
			srcFile, err := os.Open(srcPath)
			if err != nil {
				return err
			}
			defer srcFile.Close()

			destFile, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer destFile.Close()

			if _, err := io.Copy(destFile, srcFile); err != nil {
				return err
			}
		}
	}

	return nil
}
