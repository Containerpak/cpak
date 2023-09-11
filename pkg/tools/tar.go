package tools

import (
	"os"
	"os/exec"
)

func TarUnpack(srcPath, dstPath string) error {
	cmd := exec.Command("tar", "--exclude", "dev", "-xf", srcPath, "-C", dstPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
