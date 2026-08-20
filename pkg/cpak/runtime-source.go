/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const MaxRuntimeSourceSize int64 = 2 << 30

var (
	ErrRuntimeSourceInsecure = errors.New("runtime source must be served over https")
	ErrRuntimeSourceChecksum = errors.New("runtime source checksum mismatch")
	ErrRuntimeSourceSize     = errors.New("runtime source size mismatch")
)

func RuntimeSourceFileName(source types.RuntimeSource) string {
	if source.Name != "" {
		return source.Name
	}
	parsed, err := url.Parse(source.URL)
	if err != nil {
		return ""
	}
	return path.Base(parsed.Path)
}

func ValidateRuntimeSource(source types.RuntimeSource) error {
	parsed, err := url.Parse(source.URL)
	if err != nil {
		return fmt.Errorf("invalid runtime source url %q: %w", source.URL, err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%w: %s", ErrRuntimeSourceInsecure, source.URL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid runtime source url %q: no host", source.URL)
	}
	if !isSHA256(source.SHA256) {
		return fmt.Errorf("runtime source %s must declare a sha256 checksum", source.URL)
	}
	if source.Size <= 0 || source.Size > MaxRuntimeSourceSize {
		return fmt.Errorf("runtime source %s declares an invalid size of %d bytes", source.URL, source.Size)
	}
	if source.Installer != "dpkg" && source.Installer != "rpm" && source.Installer != "tar" {
		return fmt.Errorf("runtime source %s declares unsupported installer %q", source.URL, source.Installer)
	}
	name := RuntimeSourceFileName(source)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid runtime source file name %q", name)
	}
	return nil
}

type RuntimeFetcher struct {
	CacheDir string
	Client   *http.Client
	Progress func(string)
}

func (c *Cpak) NewRuntimeFetcher() *RuntimeFetcher {
	return &RuntimeFetcher{
		CacheDir: c.GetInCacheDir("runtimes"),
		Client:   &http.Client{Timeout: 30 * time.Minute},
	}
}

func (f *RuntimeFetcher) Fetch(source types.RuntimeSource) (artifact string, err error) {
	if err = ValidateRuntimeSource(source); err != nil {
		return "", err
	}
	if err = os.MkdirAll(f.CacheDir, 0755); err != nil {
		return "", err
	}

	checksum := strings.ToLower(source.SHA256)
	artifact = filepath.Join(f.CacheDir, checksum)
	if verifyRuntimeArtifact(artifact, source) == nil {
		f.report("Using cached runtime source %s", RuntimeSourceFileName(source))
		return artifact, nil
	}
	if err = os.Remove(artifact); err != nil && !os.IsNotExist(err) {
		return "", err
	}

	temp, err := os.CreateTemp(f.CacheDir, ".download-")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(temp.Name())
		}
	}()

	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	requestClient := *client
	previousRedirect := requestClient.CheckRedirect
	requestClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !strings.EqualFold(request.URL.Scheme, "https") {
			return ErrRuntimeSourceInsecure
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	f.report("Downloading runtime source %s (%d bytes)", RuntimeSourceFileName(source), source.Size)
	response, err := requestClient.Get(source.URL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch %s: %w", source.URL, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch %s: %s", source.URL, response.Status)
	}
	if response.ContentLength > source.Size {
		return "", fmt.Errorf("%w: %s declares %d bytes, server announced %d", ErrRuntimeSourceSize, source.URL, source.Size, response.ContentLength)
	}

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(response.Body, source.Size+1))
	if err != nil {
		return "", fmt.Errorf("failed to fetch %s: %w", source.URL, err)
	}
	if written != source.Size {
		return "", fmt.Errorf("%w: %s declares %d bytes, got %d", ErrRuntimeSourceSize, source.URL, source.Size, written)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != checksum {
		return "", fmt.Errorf("%w: %s expected %s, got %s", ErrRuntimeSourceChecksum, source.URL, checksum, got)
	}
	if err = temp.Sync(); err != nil {
		return "", err
	}
	if err = temp.Close(); err != nil {
		return "", err
	}
	if err = os.Chmod(temp.Name(), 0644); err != nil {
		return "", err
	}
	if err = os.Rename(temp.Name(), artifact); err != nil {
		return "", err
	}
	f.report("Verified runtime source %s", RuntimeSourceFileName(source))
	return artifact, nil
}

func (f *RuntimeFetcher) report(format string, args ...any) {
	if f.Progress != nil {
		f.Progress(fmt.Sprintf(format, args...))
	}
}

func verifyRuntimeArtifact(artifact string, source types.RuntimeSource) error {
	info, err := os.Stat(artifact)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != source.Size {
		return ErrRuntimeSourceSize
	}
	file, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(source.SHA256) {
		return ErrRuntimeSourceChecksum
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
