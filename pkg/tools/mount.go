package tools

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// IsMounted checks if the given source path is mounted in the given
// destination path. It does so by reading the /proc/mounts file.
func IsMounted(srcPath string, destPath string) (bool, error) {
	mounts, err := os.Open("/proc/mounts")
	if err != nil {
		return false, fmt.Errorf("error opening /proc/mounts: %w", err)
	}
	defer mounts.Close()

	scanner := bufio.NewScanner(mounts)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, srcPath) && strings.Contains(line, destPath) {
			return true, nil
		}
	}

	return false, nil
}

// Mount mounts the given source path in the given destination path, by
// calling the mount syscall.
func Mount(src, dest string, mode uintptr) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		_ = os.MkdirAll(dest, 0o755)
	} else {
		file, _ := os.Create(dest)

		defer func() { _ = file.Close() }()
	}

	return syscall.Mount(src, dest, "bind", mode, "")
}

// MountBind mounts bind the given source path in the given destination path.
// It is just a wrapper around Mount, for convenience.
func MountBind(src, dest string) error {
	return Mount(src, dest, syscall.MS_BIND|syscall.MS_REC|syscall.MS_PRIVATE)
}
