/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const defaultStoreIndex = "https://raw.githubusercontent.com/Containerpak/store/main/index.json"
const defaultGitHubAPI = "https://api.github.com"

type storeEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Manifest    string `json:"manifest"`
}

type storeManifest struct {
	Branch      string `json:"branch"`
	Release     string `json:"release"`
	Commit      string `json:"commit"`
	Description string `json:"description"`
}

type signedEntry struct {
	Metadata  string `json:"metadata"`
	Signature string `json:"signature"`
}

type catalog struct {
	Schema   int                               `json:"schema"`
	Release  string                            `json:"release"`
	Packages map[string]map[string]signedEntry `json:"packages"`
}

func main() {
	outputPath := flag.String("output", "", "output path")
	release := flag.String("release", "", "cpak release")
	indexURL := flag.String("store-index", defaultStoreIndex, "Store index URL")
	installerDir := flag.String("installer-dir", "dist", "directory containing packed installers")
	flag.Parse()
	if *outputPath == "" || *release == "" {
		fail(fmt.Errorf("output and release are required"))
	}
	privateKeyPEM := os.Getenv("CPAK_INSTALLER_PRIVATE_KEY")
	if privateKeyPEM == "" {
		fail(fmt.Errorf("CPAK_INSTALLER_PRIVATE_KEY is required"))
	}
	privateKey, err := bootstrap.ParsePrivateKeyPEM([]byte(privateKeyPEM))
	if err != nil {
		fail(err)
	}

	digests, err := installerDigests(*installerDir)
	if err != nil {
		fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	result, err := buildCatalog(ctx, client, *indexURL, defaultGitHubAPI, *release, digests, privateKey)
	if err != nil {
		fail(err)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail(err)
	}
	encoded = append(encoded, '\n')
	if err = os.WriteFile(*outputPath, encoded, 0644); err != nil {
		fail(err)
	}
}

func buildCatalog(ctx context.Context, client *http.Client, indexURL, githubAPI, release string, digests map[string]string, privateKey ed25519.PrivateKey) (catalog, error) {
	var index map[string]map[string]storeEntry
	if err := fetchJSON(ctx, client, indexURL, &index); err != nil {
		return catalog{}, err
	}
	result := catalog{Schema: 1, Release: release, Packages: map[string]map[string]signedEntry{}}
	for _, entries := range index {
		for origin, entry := range entries {
			var manifest storeManifest
			if err := fetchJSON(ctx, client, entry.Manifest, &manifest); err != nil {
				return catalog{}, fmt.Errorf("load %s: %w", origin, err)
			}
			_, ref := selectedReference(manifest)
			if ref == "" {
				return catalog{}, fmt.Errorf("package reference is missing: %s", origin)
			}
			commit, err := resolveCommit(ctx, client, githubAPI, origin, ref)
			if err != nil {
				return catalog{}, fmt.Errorf("resolve %s: %w", origin, err)
			}
			packageManifest, err := loadPackageManifest(ctx, client, githubAPI, origin, commit)
			if err != nil {
				return catalog{}, fmt.Errorf("load %s package manifest: %w", origin, err)
			}
			iconBase := strings.TrimSuffix(entry.Manifest, path.Base(entry.Manifest))
			icon, rasterIcon, err := loadIcon(ctx, client, iconBase)
			if err != nil {
				return catalog{}, fmt.Errorf("load %s icon: %w", origin, err)
			}
			description := strings.TrimSpace(manifest.Description)
			if description == "" {
				description = strings.TrimSpace(entry.Description)
			}
			if description == "" {
				description = "cpak application"
			}
			result.Packages[origin] = map[string]signedEntry{}
			for _, arch := range []string{"amd64", "arm64"} {
				digest := digests[arch]
				if digest == "" {
					return catalog{}, fmt.Errorf("installer digest is missing for %s", arch)
				}
				metadata := bootstrap.Metadata{
					Schema:          bootstrap.SchemaVersion,
					Origin:          origin,
					Name:            truncate(entry.Name, 120),
					Description:     truncate(description, 500),
					IconSVG:         icon,
					IconPNG:         rasterIcon,
					Permissions:     summarizePermissions(packageManifest.Override),
					RefType:         "commit",
					Ref:             commit,
					Arch:            arch,
					InstallerSHA256: digest,
				}
				encoded, signature, err := bootstrap.SignMetadata(metadata, privateKey)
				if err != nil {
					return catalog{}, fmt.Errorf("sign %s for %s: %w", origin, arch, err)
				}
				result.Packages[origin][arch] = signedEntry{
					Metadata:  base64.StdEncoding.EncodeToString(encoded),
					Signature: base64.StdEncoding.EncodeToString(signature),
				}
			}
		}
	}
	return result, nil
}

func loadPackageManifest(ctx context.Context, client *http.Client, githubAPI, origin, commit string) (*types.CpakManifest, error) {
	parts := strings.Split(origin, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("invalid package origin: %s", origin)
	}
	endpoint := strings.TrimSuffix(githubAPI, "/") + "/repos/" + parts[1] + "/" + parts[2] + "/contents/cpak.json?ref=" + url.QueryEscape(commit)
	encoded, err := fetch(ctx, client, endpoint, 2*1024*1024)
	if err != nil {
		return nil, err
	}
	manifest, err := cpak.DecodeManifest(encoded)
	if err != nil {
		return nil, err
	}
	if err := (&cpak.Cpak{}).ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func summarizePermissions(override types.Override) []bootstrap.Permission {
	permissions := []bootstrap.Permission{}
	add := func(enabled bool, name, detail string) {
		if enabled {
			permissions = append(permissions, bootstrap.Permission{Name: name, Detail: detail})
		}
	}

	displays := []string{}
	if override.SocketX11 {
		displays = append(displays, "X11")
	}
	if override.SocketWayland {
		displays = append(displays, "Wayland")
	}
	if len(displays) > 0 {
		permissions = append(permissions, bootstrap.Permission{Name: "Display", Detail: strings.Join(displays, ", ")})
	}
	add(override.SocketPulseAudio, "Audio", "PulseAudio")
	add(override.SocketSessionBus, "Session services", "session D-Bus")
	add(override.SocketSystemBus, "System services", "system D-Bus")
	add(override.SocketSshAgent, "SSH agent", "host authentication socket")
	add(override.SocketCups, "Printing", "CUPS")
	add(override.SocketGpgAgent, "GPG agent", "host signing socket")
	add(override.SocketAtSpiBus, "Accessibility", "AT-SPI")
	add(override.SocketBluetooth, "Bluetooth", "Bluetooth socket")

	devices := []string{}
	if override.DeviceAll {
		devices = append(devices, "all devices")
	} else {
		deviceFlags := []struct {
			enabled bool
			name    string
		}{
			{override.DeviceDri, "graphics"},
			{override.DeviceKvm, "KVM"},
			{override.DeviceShm, "shared memory"},
			{override.DeviceAlsa, "ALSA"},
			{override.DeviceVideo, "video"},
			{override.DeviceFuse, "FUSE"},
			{override.DeviceTun, "TUN/TAP"},
			{override.DeviceUsb, "USB"},
			{override.DeviceInput, "input devices"},
			{override.DeviceTTY, "controlling terminal"},
		}
		for _, device := range deviceFlags {
			if device.enabled {
				devices = append(devices, device.name)
			}
		}
	}
	if len(devices) > 0 {
		permissions = append(permissions, bootstrap.Permission{Name: "Devices", Detail: strings.Join(devices, ", ")})
	}

	add(override.Notification, "Notifications", "desktop notifications")
	add(override.OpenURI, "External links", "open URIs on the host")
	add(override.HostApplications, "Host applications", "desktop catalog and launch broker")
	for _, filesystem := range override.Filesystem {
		permissions = append(permissions, bootstrap.Permission{
			Name:   "Files",
			Detail: filesystem.Path + ", " + strings.ReplaceAll(filesystem.Access, "-", " "),
		})
	}
	add(override.FsHost, "Files", "host, read only")
	add(override.FsHostEtc, "Files", "/etc, read only")
	add(override.FsHostHome, "Files", "home, read and write")
	for _, filesystem := range override.FsExtra {
		permissions = append(permissions, bootstrap.Permission{Name: "Files", Detail: filesystem + ", read and write"})
	}
	add(override.Network, "Network", "internet and local network")
	add(override.Process, "Host processes", "shared process namespace")
	add(override.UserNamespaces, "Nested sandboxes", "user namespaces")
	add(override.AsRoot, "Root", "runs as root inside the cpak")
	for _, action := range override.HostActions {
		permissions = append(permissions, bootstrap.Permission{
			Name:   "Host service",
			Detail: truncate(action.Provider+": "+strings.Join(action.Capabilities, ", "), 160),
		})
	}
	return permissions
}

func installerDigests(dir string) (map[string]string, error) {
	result := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		encoded, err := os.ReadFile(filepath.Join(dir, "cpak-installer-linux-"+arch))
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(encoded)
		result[arch] = hex.EncodeToString(digest[:])
	}
	return result, nil
}

func resolveCommit(ctx context.Context, client *http.Client, githubAPI, origin, ref string) (string, error) {
	parts := strings.Split(origin, "/")
	if len(parts) != 3 || parts[0] != "github.com" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("invalid package origin: %s", origin)
	}
	endpoint := strings.TrimSuffix(githubAPI, "/") + "/repos/" + parts[1] + "/" + parts[2] + "/commits/" + url.PathEscape(ref)
	var result struct {
		SHA string `json:"sha"`
	}
	if err := fetchJSON(ctx, client, endpoint, &result); err != nil {
		return "", err
	}
	if len(result.SHA) != 40 {
		return "", errors.New("GitHub returned an invalid commit")
	}
	return result.SHA, nil
}

func selectedReference(manifest storeManifest) (string, string) {
	if manifest.Branch != "" {
		return "branch", manifest.Branch
	}
	if manifest.Release != "" {
		return "release", manifest.Release
	}
	if manifest.Commit != "" {
		return "commit", manifest.Commit
	}
	return "", ""
}

func fetchJSON(ctx context.Context, client *http.Client, url string, target any) error {
	encoded, err := fetch(ctx, client, url, 2*1024*1024)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

// loadIcon answers with the icon a package publishes. An SVG is preferred
// because it scales, but an upstream whose only mark is a bitmap must still be
// able to show its own icon, and redrawing someone else's mark as a vector is
// not something a catalog build gets to decide. Exactly one of the two is
// returned, and a package with neither is an error rather than a silent
// placeholder.
func loadIcon(ctx context.Context, client *http.Client, base string) (string, string, error) {
	vector, err := fetchText(ctx, client, base+"icon.svg", 512*1024)
	if err == nil {
		return vector, "", nil
	}
	if !errors.Is(err, errNotFound) {
		return "", "", err
	}
	raster, err := fetch(ctx, client, base+"icon.png", 1024*1024)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return "", "", fmt.Errorf("neither icon.svg nor icon.png is published")
		}
		return "", "", err
	}
	return "", base64.StdEncoding.EncodeToString(raster), nil
}

func fetchText(ctx context.Context, client *http.Client, url string, limit int64) (string, error) {
	encoded, err := fetch(ctx, client, url, limit)
	return string(encoded), err
}

func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		encoded, retry, err := fetchOnce(ctx, client, url, limit)
		if err == nil || !retry {
			return encoded, err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
		}
	}
	return nil, lastErr
}

var errNotFound = errors.New("not published")

func fetchOnce(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" && request.URL.Host == "api.github.com" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if strings.Contains(request.URL.Path, "/contents/") {
		request.Header.Set("Accept", "application/vnd.github.raw+json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, true, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		// A caller looking for an optional file has to tell a file that is not
		// published from a fetch that went wrong, so that a network fault is
		// never read as an absence.
		if response.StatusCode == http.StatusNotFound {
			return nil, false, fmt.Errorf("%s: %w", url, errNotFound)
		}
		return nil, retry, fmt.Errorf("%s returned %s", url, response.Status)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(encoded)) > limit {
		return nil, false, fmt.Errorf("%s exceeds %d bytes", url, limit)
	}
	return encoded, false, nil
}

func truncate(value string, length int) string {
	runes := []rune(value)
	if len(runes) <= length {
		return value
	}
	return string(runes[:length])
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
