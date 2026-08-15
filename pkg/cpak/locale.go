/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	localeImageLabel     = "io.containerpak.locale.image"
	platformVersionLabel = "io.containerpak.platform.version"
	ubuntuImageLabel     = "io.containerpak.ubuntu.image"
	maxLocaleLayerSize   = 128 << 20
)

// BuildLocaleLayer adds the compiled host locale supplied by the application
// platform. User environment overrides may select another locale.
func (c *Cpak) BuildLocaleLayer(layers []string, image, config string, override types.Override) ([]string, error) {
	result := append([]string{}, layers...)
	environment := append([]string{}, os.Environ()...)
	environment = append(environment, override.Env...)
	prefixes := localeLayerPaths(environment)
	if len(prefixes) == 0 {
		return result, nil
	}

	reference, err := localeImageReference(image, config)
	if err != nil || reference == "" {
		return result, err
	}
	digest, err := c.pullLocaleLayer(reference, prefixes)
	if err != nil {
		return nil, err
	}
	for _, layer := range result {
		if layer == digest {
			return result, nil
		}
	}
	return append(result, digest), nil
}

func localeImageReference(image, config string) (string, error) {
	file := &oci.ConfigFile{}
	if err := json.Unmarshal([]byte(config), file); err != nil {
		return "", fmt.Errorf("decode locale image config: %w", err)
	}
	labels := file.Config.Labels
	if reference := labels[localeImageLabel]; reference != "" {
		if err := tools.ValidateImageName(reference); err != nil {
			return "", fmt.Errorf("invalid locale image %q: %w", reference, err)
		}
		return reference, nil
	}
	if labels[ubuntuImageLabel] == "" {
		if !strings.HasPrefix(strings.ToLower(image), "ghcr.io/containerpak/") {
			return "", nil
		}
		return "ghcr.io/containerpak/locales:ubuntu-26.04", nil
	}
	parts := strings.Split(labels[platformVersionLabel], ".")
	if len(parts) < 2 || !decimal(parts[0]) || !decimal(parts[1]) {
		return "", nil
	}
	return "ghcr.io/containerpak/locales:ubuntu-" + parts[0] + "." + parts[1], nil
}

func (c *Cpak) pullLocaleLayer(reference string, prefixes []string) (string, error) {
	client := &oci.Client{}
	image, err := client.Resolve(c.Ctx, reference)
	if err != nil {
		return "", fmt.Errorf("resolve locale image %s: %w", reference, err)
	}
	if len(image.Layers) != 1 {
		return "", fmt.Errorf("locale image %s must contain one layer", reference)
	}
	layer := image.Layers[0]
	if layer.Size <= 0 || layer.Size > maxLocaleLayerSize {
		return "", fmt.Errorf("locale image %s has an invalid layer size of %d bytes", reference, layer.Size)
	}

	digest := localeLayerDigest(layer.Digest, prefixes)
	available, err := c.layerAvailable(digest)
	if err != nil || available {
		return digest, err
	}

	content, err := client.ResumableBlob(c.Ctx, image.Reference, layer)
	if err != nil {
		return "", fmt.Errorf("open locale layer: %w", err)
	}
	defer content.Close()
	blob, err := os.CreateTemp(c.Options.CachePath, ".locale-layer-")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = blob.Close()
		_ = os.Remove(blob.Name())
	}()

	hash := sha256.New()
	limited := &io.LimitedReader{R: content, N: layer.Size}
	received, err := io.Copy(io.MultiWriter(blob, hash), limited)
	if err != nil {
		return "", err
	}
	if limited.N != 0 {
		return "", fmt.Errorf("locale layer size mismatch: expected %d, received %d", layer.Size, received)
	}
	extra, err := io.Copy(io.Discard, io.LimitReader(content, 1))
	if err != nil {
		return "", err
	}
	if extra != 0 {
		return "", fmt.Errorf("locale layer size mismatch: expected %d, received more than %d", layer.Size, layer.Size)
	}
	want := strings.TrimPrefix(layer.Digest, "sha256:")
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return "", fmt.Errorf("locale layer digest mismatch: expected %s, got %s", want, got)
	}
	if _, err = blob.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	prefixes, err = layerPathsIncludingLinks(blob, layer.MediaType, prefixes)
	if err != nil {
		return "", err
	}
	if _, err = blob.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	temporary, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "locale " + strings.Join(prefixes, ","),
		ComputeSHA256: true,
	})
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = writer.Abort()
		}
		_ = os.RemoveAll(temporary)
	}()

	selected, extractErr := unpackLayerPaths(c.Ctx, blob, layer.MediaType, writer, prefixes)
	if extractErr != nil {
		return "", extractErr
	}
	if selected == 0 {
		return "", fmt.Errorf("locale image %s does not contain %s", reference, strings.Join(prefixes, ", "))
	}
	if _, err = writer.Commit(); err != nil {
		return "", err
	}
	committed = true
	if err = publishFVSLayer(temporary, c.fvsLayerPath(digest)); err != nil {
		return "", err
	}
	logger.Printf("Locale layer ready for %s", strings.Join(prefixes, ", "))
	return digest, nil
}

func localeLayerDigest(source string, prefixes []string) string {
	values := append([]string{}, prefixes...)
	sort.Strings(values)
	hash := sha256.New()
	_, _ = hash.Write([]byte("locale-layer-v2"))
	_, _ = hash.Write([]byte(source))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func localeLayerPaths(environment []string) []string {
	values := localeValues(environment)
	paths := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		path := localeLayerPath(value)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func localeValues(environment []string) []string {
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found || name != "LANG" && name != "LC_ALL" && !strings.HasPrefix(name, "LC_") {
			continue
		}
		values[name] = value
	}
	if value := values["LC_ALL"]; value != "" {
		return []string{value}
	}
	result := []string{}
	if value := values["LANG"]; value != "" {
		result = append(result, value)
	}
	keys := make([]string, 0, len(values))
	for name, value := range values {
		if strings.HasPrefix(name, "LC_") && name != "LC_ALL" && value != "" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	for _, name := range keys {
		result = append(result, values[name])
	}
	return result
}

func localeLayerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "C") || strings.EqualFold(value, "POSIX") || strings.EqualFold(value, "C.UTF-8") || strings.EqualFold(value, "C.UTF8") {
		return ""
	}
	if !localeName(value) {
		return ""
	}

	modifier := ""
	if index := strings.IndexByte(value, '@'); index >= 0 {
		modifier = value[index:]
		value = value[:index]
	}
	encoding := ""
	if index := strings.IndexByte(value, '.'); index >= 0 {
		encoding = value[index+1:]
		value = value[:index]
	}
	if value == "" {
		return ""
	}
	if normalized := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(encoding)); normalized == "utf8" {
		value += ".utf8"
	}
	return filepath.ToSlash(filepath.Join("usr", "lib", "locale", value+modifier))
}

func localeName(value string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '_', '-', '.', '@':
			continue
		}
		return false
	}
	return true
}

func decimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func inheritHostLocale(environment, host []string) []string {
	values := make(map[string]string)
	for _, entry := range host {
		name, value, found := strings.Cut(entry, "=")
		if found && localeEnvironmentVariable(name) {
			values[name] = value
		}
	}
	result := make([]string, 0, len(environment)+len(values))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && localeEnvironmentVariable(name) {
			continue
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

func localeEnvironmentVariable(name string) bool {
	return name == "LANG" || name == "LANGUAGE" || name == "LC_ALL" || strings.HasPrefix(name, "LC_")
}
