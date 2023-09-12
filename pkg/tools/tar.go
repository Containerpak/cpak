package tools

import (
	"os"
	"os/exec"
)

// TarPack packs the given directory into a tarball.
//
// Note: we are not using the tar package from the standard library
// because it does not support tarballs with an unknown header type.
func TarUnpack(srcPath, dstPath string) error {
	// TODO: find a way to use the tar package from the standard library
	// instead of relying on the tar command
	cmd := exec.Command("tar", "--exclude", "dev", "-xf", srcPath, "-C", dstPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
