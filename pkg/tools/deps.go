package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed rootlesskit.tar.gz
var rootlesskit []byte

func EnsureUnixDeps(binPath string, rootlessImplementation string) error {
	err := os.MkdirAll(binPath, 0755)
	if err != nil {
		return fmt.Errorf("error creating bin directory: %w", err)
	}

	switch rootlessImplementation {
	case "rootlesskit":
		_, err := os.Stat(filepath.Join(binPath, "rootlesskit"))
		if err == nil {
			return nil
		}
		gzipReader, err := gzip.NewReader(bytes.NewReader(rootlesskit))
		if err != nil {
			return fmt.Errorf("error creating gzip reader: %w", err)
		}
		defer gzipReader.Close()

		tarReader := tar.NewReader(gzipReader)
		for {
			header, err := tarReader.Next()
			if err != nil {
				if err == io.EOF {
					break
				}

				return fmt.Errorf("error reading rootlesskit tar: %w", err)
			}

			if header.Typeflag != tar.TypeReg {
				continue
			}

			filePath := filepath.Join(binPath, header.Name)
			file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("error opening file: %w", err)
			}
			defer file.Close()

			_, err = io.Copy(file, tarReader)
			if err != nil {
				return fmt.Errorf("error copying file: %w", err)
			}
		}

		// making binaries executable
		bins := []string{"rootlessctl", "rootlesskit", "rootlesskit-docker-proxy"}
		for _, bin := range bins {
			binPath := filepath.Join(binPath, bin)
			err = os.Chmod(binPath, 0755)
			if err != nil {
				return fmt.Errorf("error setting permissions on %s: %w", bin, err)
			}
		}

	}

	return nil
}
