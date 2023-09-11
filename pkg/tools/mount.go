package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func MountOverlay(layerPath string, destPath string) error {
	cmd := exec.Command("mount", "-t", "overlay", "overlay", "-o", "lowerdir="+layerPath+",upperdir="+destPath+",workdir="+filepath.Join(destPath, "work"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func MountVfs(layerPath string, destPath string) error {
	cmd := exec.Command("mount", "--bind", layerPath, destPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func MountScratch(layerPath string, destPath string) error {
	//err := copyDirectory(layerPath, containerPath)
	err := exec.Command("cp", "-r", layerPath, destPath).Run()
	if err != nil {
		return fmt.Errorf("error copying layer contents: %w", err)
	}

	return nil
}

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

func MountBind(src, dest string) error {
	return Mount(src, dest, syscall.MS_BIND|syscall.MS_REC|syscall.MS_PRIVATE)
}
