/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPI = "https://api.github.com/repos/Containerpak/cpak/releases/latest"
	maxBinary  = 128 << 20
)

// ErrManagedInstall indicates that the package manager owns the cpak binary.
var ErrManagedInstall = errors.New("selfupdate: cpak is managed by the system package manager")

// Release contains the latest published cpak release.
type Release struct {
	Version      string    `json:"version"`
	Notes        string    `json:"notes"`
	PublishedAt  time.Time `json:"published_at"`
	BinaryURL    string    `json:"binary_url"`
	StorageURL   string    `json:"storage_url"`
	ChecksumsURL string    `json:"checksums_url"`
}

// Checker resolves and installs cpak releases.
type Checker struct {
	CurrentVersion string
	Mode           string
	APIURL         string
	CachePath      string
	Executable     string
	HTTP           *http.Client
	Migrate        func(context.Context, string) error
}

type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Release   Release   `json:"release"`
	Notified  string    `json:"notified,omitempty"`
}

// Check returns the latest release, using a recent cached response when possible.
func (c Checker) Check(ctx context.Context, maxAge time.Duration) (Release, bool, error) {
	state, _ := readCache(c.cachePath())
	storageMissing := c.storageServiceMissing()
	if state.Release.Version != "" && time.Since(state.CheckedAt) < maxAge && (!storageMissing || state.Release.StorageURL != "") {
		return state.Release, newer(state.Release.Version, c.CurrentVersion) || storageMissing, nil
	}
	release, err := c.fetch(ctx)
	if err != nil {
		if state.Release.Version != "" {
			return state.Release, newer(state.Release.Version, c.CurrentVersion) || storageMissing && state.Release.StorageURL != "", nil
		}
		return Release{}, false, err
	}
	state.CheckedAt = time.Now().UTC()
	state.Release = release
	if err = writeCache(c.cachePath(), state); err != nil {
		return Release{}, false, err
	}
	return release, newer(release.Version, c.CurrentVersion) || storageMissing && release.StorageURL != "", nil
}

func (c Checker) storageServiceMissing() bool {
	executable := c.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return true
		}
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(executable), "cpak-storaged"))
	return err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0
}

// Install downloads, verifies and atomically replaces the current cpak binary.
func (c Checker) Install(ctx context.Context, release Release) error {
	if c.Mode == "managed" {
		return ErrManagedInstall
	}
	if release.BinaryURL == "" || release.StorageURL == "" || release.ChecksumsURL == "" {
		return fmt.Errorf("selfupdate: release has no compatible binary")
	}
	executable := c.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}
	checksums, err := c.download(ctx, release.ChecksumsURL, 1<<20)
	if err != nil {
		return fmt.Errorf("selfupdate: download checksums: %w", err)
	}
	asset := "cpak-linux-" + runtime.GOARCH
	expected, err := checksumFor(checksums, asset)
	if err != nil {
		return err
	}
	binary, err := c.download(ctx, release.BinaryURL, maxBinary)
	if err != nil {
		return fmt.Errorf("selfupdate: download cpak: %w", err)
	}
	digest := sha256.Sum256(binary)
	if fmt.Sprintf("%x", digest[:]) != expected {
		return fmt.Errorf("selfupdate: binary checksum mismatch")
	}
	companionAsset := "cpak-storaged-linux-" + runtime.GOARCH
	companionExpected, err := checksumFor(checksums, companionAsset)
	if err != nil {
		return err
	}
	companion, err := c.download(ctx, release.StorageURL, maxBinary)
	if err != nil {
		return fmt.Errorf("selfupdate: download storage service: %w", err)
	}
	companionDigest := sha256.Sum256(companion)
	if fmt.Sprintf("%x", companionDigest[:]) != companionExpected {
		return fmt.Errorf("selfupdate: storage service checksum mismatch")
	}
	directory := filepath.Dir(executable)
	if err = replaceExecutable(filepath.Join(directory, "cpak-storaged"), companion); err != nil {
		return fmt.Errorf("selfupdate: replace storage service: %w", err)
	}
	if err = replaceExecutable(executable, binary); err != nil {
		return fmt.Errorf("selfupdate: replace cpak: %w", err)
	}
	if err = c.migrate(ctx, executable); err != nil {
		return fmt.Errorf("selfupdate: migrate storage: %w", err)
	}
	return nil
}

func (c Checker) migrate(ctx context.Context, executable string) error {
	if c.Migrate != nil {
		return c.Migrate(ctx, executable)
	}
	command := exec.CommandContext(ctx, executable, "storage", "migrate")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func replaceExecutable(target string, payload []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".cpak-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0755); err == nil {
		_, err = temporary.Write(payload)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

// MarkNotified records that the desktop update prompt was shown.
func (c Checker) MarkNotified(version string) error {
	state, _ := readCache(c.cachePath())
	state.Notified = version
	return writeCache(c.cachePath(), state)
}

// WasNotified reports whether the desktop prompt has already shown this release.
func (c Checker) WasNotified(version string) bool {
	state, err := readCache(c.cachePath())
	return err == nil && state.Notified == version
}

func (c Checker) fetch(ctx context.Context) (Release, error) {
	api := c.APIURL
	if api == "" {
		api = defaultAPI
	}
	if err := validateUpdateURL(api); err != nil {
		return Release{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "cpak")
	response, err := c.updateClient().Do(request)
	if err != nil {
		return Release{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("selfupdate: release lookup failed: %s", response.Status)
	}
	var payload struct {
		TagName     string    `json:"tag_name"`
		Body        string    `json:"body"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("selfupdate: decode release: %w", err)
	}
	release := Release{Version: payload.TagName, Notes: payload.Body, PublishedAt: payload.PublishedAt}
	wanted := "cpak-linux-" + runtime.GOARCH
	companion := "cpak-storaged-linux-" + runtime.GOARCH
	for _, asset := range payload.Assets {
		switch asset.Name {
		case wanted:
			release.BinaryURL = asset.URL
		case companion:
			release.StorageURL = asset.URL
		case "SHA256SUMS":
			release.ChecksumsURL = asset.URL
		}
	}
	if _, valid := parseVersion(release.Version); !valid {
		return Release{}, fmt.Errorf("selfupdate: invalid release version %q", release.Version)
	}
	return release, nil
}

func (c Checker) download(ctx context.Context, target string, limit int64) ([]byte, error) {
	if err := validateUpdateURL(target); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "cpak")
	response, err := c.updateClient().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response: %s", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return content, nil
}

func (c Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c Checker) updateClient() *http.Client {
	client := *c.client()
	configured := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("selfupdate: too many redirects")
		}
		if err := validateUpdateURL(request.URL.String()); err != nil {
			return err
		}
		if configured != nil {
			return configured(request, via)
		}
		return nil
	}
	return &client
}

func validateUpdateURL(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("selfupdate: invalid download URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && updateLoopback(parsed.Host) {
		return nil
	}
	return errors.New("selfupdate: download URL requires HTTPS")
}

func updateLoopback(host string) bool {
	hostname := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsed
	}
	address := net.ParseIP(hostname)
	return hostname == "localhost" || address != nil && address.IsLoopback()
}

func (c Checker) cachePath() string {
	if c.CachePath != "" {
		return c.CachePath
	}
	directory, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "cpak-self-update.json")
	}
	return filepath.Join(directory, "cpak", "self-update.json")
}

func readCache(path string) (cache, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return cache{}, err
	}
	var state cache
	if err = json.Unmarshal(content, &state); err != nil {
		return cache{}, err
	}
	return state, nil
}

func writeCache(path string, state cache) error {
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".self-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(content)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func checksumFor(content []byte, asset string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset && len(fields[0]) == sha256.Size*2 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("selfupdate: checksum for %s is missing", asset)
}

func newer(candidate, current string) bool {
	wanted, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	have, ok := parseVersion(current)
	if !ok {
		return false
	}
	for index := range wanted.numbers {
		if wanted.numbers[index] != have.numbers[index] {
			return wanted.numbers[index] > have.numbers[index]
		}
	}
	if wanted.prerelease == have.prerelease {
		return false
	}
	return wanted.prerelease == ""
}

type semanticVersion struct {
	numbers    [3]int
	prerelease string
}

func parseVersion(value string) (semanticVersion, bool) {
	value = strings.TrimPrefix(value, "v")
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		prerelease := value[dash+1:]
		if prerelease == "" {
			return semanticVersion{}, false
		}
		version, valid := parseVersionNumbers(value[:dash])
		version.prerelease = prerelease
		return version, valid
	}
	return parseVersionNumbers(value)
}

func parseVersionNumbers(value string) (semanticVersion, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	var version semanticVersion
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return semanticVersion{}, false
		}
		version.numbers[index] = parsed
	}
	return version, true
}
