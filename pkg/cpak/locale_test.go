/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const testOCIManifestMediaType = "application/vnd.oci.image.manifest.v1+json"

func TestLocaleLayerPaths(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "utf8", value: "pt_BR.UTF-8", want: "usr/lib/locale/pt_BR.utf8"},
		{name: "modifier", value: "sr_RS.UTF-8@latin", want: "usr/lib/locale/sr_RS.utf8@latin"},
		{name: "legacy", value: "pt_BR.ISO-8859-1", want: "usr/lib/locale/pt_BR"},
		{name: "posix", value: "C.UTF-8"},
		{name: "path", value: "../../pt_BR.UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := localeLayerPath(test.value); got != test.want {
				t.Fatalf("locale path = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLocaleValuesHonorLCAll(t *testing.T) {
	environment := []string{"LANG=en_US.UTF-8", "LC_MESSAGES=de_DE.UTF-8", "LC_ALL=pt_BR.UTF-8"}
	values := localeValues(environment)
	if len(values) != 1 || values[0] != "pt_BR.UTF-8" {
		t.Fatalf("locale values = %v", values)
	}
}

func TestInheritHostLocaleReplacesImageDefaults(t *testing.T) {
	image := []string{"PATH=/usr/bin", "LANG=C.UTF-8", "LC_MESSAGES=C"}
	host := []string{"LANG=pt_BR.UTF-8", "LANGUAGE=pt_BR"}
	got := inheritHostLocale(image, host)
	joined := strings.Join(got, "\n")
	for _, value := range []string{"PATH=/usr/bin", "LANG=pt_BR.UTF-8", "LANGUAGE=pt_BR"} {
		if !strings.Contains(joined, value) {
			t.Fatalf("environment is missing %q: %v", value, got)
		}
	}
	if strings.Contains(joined, "LANG=C.UTF-8") || strings.Contains(joined, "LC_MESSAGES=C") {
		t.Fatalf("image locale remained in environment: %v", got)
	}
}

func TestLocaleImageReference(t *testing.T) {
	explicit := `{"config":{"Labels":{"io.containerpak.locale.image":"ghcr.io/example/locales:test"}}}`
	if got, err := localeImageReference("ghcr.io/example/app:main", explicit); err != nil || got != "ghcr.io/example/locales:test" {
		t.Fatalf("explicit locale image = %q, %v", got, err)
	}
	fallback := `{"config":{"Labels":{"io.containerpak.platform.version":"26.04.20260814.2","io.containerpak.ubuntu.image":"ubuntu"}}}`
	if got, err := localeImageReference("ghcr.io/example/app:main", fallback); err != nil || got != "ghcr.io/containerpak/locales:ubuntu-26.04" {
		t.Fatalf("fallback locale image = %q, %v", got, err)
	}
	if got, err := localeImageReference("ghcr.io/example/app:main", `{"config":{}}`); err != nil || got != "" {
		t.Fatalf("unlabelled image selected %q, %v", got, err)
	}
	if got, err := localeImageReference("ghcr.io/containerpak/cpu-x:main", `{"config":{}}`); err != nil || got != "ghcr.io/containerpak/locales:ubuntu-26.04" {
		t.Fatalf("official image locale = %q, %v", got, err)
	}
}

func TestBuildLocaleLayerExtractsOnlyHostLocale(t *testing.T) {
	layer := testLayer(t, mediaOCILayerGzip, []testLayerEntry{
		{name: "usr", typeflag: tar.TypeDir, mode: 0755},
		{name: "usr/lib", typeflag: tar.TypeDir, mode: 0755},
		{name: "usr/lib/locale", typeflag: tar.TypeDir, mode: 0755},
		{name: "usr/lib/locale/pt_BR.utf8", typeflag: tar.TypeDir, mode: 0755},
		{name: "usr/lib/locale/pt_BR.utf8/LC_CTYPE", typeflag: tar.TypeReg, mode: 0644, content: []byte("portuguese")},
		{name: "usr/lib/locale/de_DE.utf8", typeflag: tar.TypeDir, mode: 0755},
		{name: "usr/lib/locale/de_DE.utf8/LC_CTYPE", typeflag: tar.TypeReg, mode: 0644, content: []byte("german")},
	})
	layerDigest := testDigest(layer)
	config := []byte(`{"architecture":"amd64","os":"linux","config":{}}`)
	configDigest := testDigest(config)
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     testOCIManifestMediaType,
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    configDigest,
			"size":      len(config),
		},
		"layers": []map[string]any{{
			"mediaType": mediaOCILayerGzip,
			"digest":    layerDigest,
			"size":      len(layer),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	layerRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/example/locales/manifests/test":
			writer.Header().Set("Content-Type", testOCIManifestMediaType)
			_, _ = writer.Write(manifest)
		case "/v2/example/locales/blobs/" + configDigest:
			_, _ = writer.Write(config)
		case "/v2/example/locales/blobs/" + layerDigest:
			layerRequests++
			_, _ = writer.Write(layer)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	t.Setenv("LANG", "pt_BR.UTF-8")
	t.Setenv("LC_ALL", "")
	appConfig := `{"config":{"Labels":{"io.containerpak.locale.image":"` + strings.TrimPrefix(server.URL, "http://") + `/example/locales:test"}}}`
	c := newTestCpak(t)
	override := types.Override{Env: []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}}
	layers, err := c.BuildLocaleLayer([]string{"base"}, "ghcr.io/example/app:test", appConfig, override)
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 || layers[0] != "base" {
		t.Fatalf("application layers = %v", layers)
	}
	states, err := fvsrepo.States(c.fvsLayerPath(layers[1]))
	if err != nil || len(states) != 1 {
		t.Fatalf("locale layer states = %v, %v", states, err)
	}
	files, err := fvsrepo.StateFiles(c.fvsLayerPath(layers[1]), states[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		if file.Path == "usr/lib/locale/pt_BR.utf8/LC_CTYPE" {
			found = true
		}
		if strings.Contains(file.Path, "de_DE") {
			t.Fatalf("unselected locale was stored: %s", file.Path)
		}
	}
	if !found {
		t.Fatal("selected locale is missing")
	}
	if _, err = c.BuildLocaleLayer([]string{"base"}, "ghcr.io/example/app:test", appConfig, override); err != nil {
		t.Fatal(err)
	}
	if layerRequests != 1 {
		t.Fatalf("locale layer downloads = %d, want 1", layerRequests)
	}
}

func testDigest(content []byte) string {
	hash := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func TestInheritHostLocaleKeepsApplicationValuesWithoutHostLocale(t *testing.T) {
	application := []string{"PATH=/usr/bin", "LANG=C.UTF-8"}
	got := inheritHostLocale(application, []string{"PATH=/bin", "HOME=/root"})
	if !slices.Equal(got, application) {
		t.Fatalf("the environment changed while the host declared no locale: %v", got)
	}
}

func TestNormalizeLocaleEnvironmentKeepsTheLastAssignment(t *testing.T) {
	environment := []string{"LANG=ru_RU.UTF-8", "LC_NUMERIC=ru_RU.UTF-8", "PATH=/usr/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	got := normalizeLocaleEnvironment(environment)
	if !slices.Contains(got, "LANG=C.UTF-8") || !slices.Contains(got, "LC_ALL=C.UTF-8") {
		t.Fatalf("application locale is missing: %v", got)
	}
	if slices.Contains(got, "LANG=ru_RU.UTF-8") {
		t.Fatalf("host locale survived: %v", got)
	}
	if slices.Contains(got, "LC_NUMERIC=ru_RU.UTF-8") {
		t.Fatalf("a host locale category survived LC_ALL: %v", got)
	}
}

func TestHostLocaleWinsOverAManifestDeclaration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := types.Application{
		Origin:         "github.com/containerpak/bottles",
		Version:        "66.1",
		ParsedOverride: types.Override{Env: []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}},
	}
	if !hostLocaleWins(app) {
		t.Fatal("a locale declared by the manifest was read as a deliberate user choice")
	}
	environment := append([]string{"PATH=/usr/bin"}, app.ParsedOverride.Env...)
	joined := strings.Join(inheritHostLocale(environment, []string{"LANG=pt_BR.UTF-8"}), "\n")
	if !strings.Contains(joined, "LANG=pt_BR.UTF-8") {
		t.Fatalf("the host locale did not reach the application: %s", joined)
	}
	if strings.Contains(joined, "C.UTF-8") {
		t.Fatalf("the manifest locale survived the host one: %s", joined)
	}
}

func TestHostLocaleWinsWithoutAnApplicationChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := types.Application{
		Origin:  "github.com/containerpak/bottles",
		Version: "66.1",
	}
	if !hostLocaleWins(app) {
		t.Fatal("an application without a locale did not inherit the host")
	}
}

func TestLocaleChosenByTheUserBeatsTheHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app := types.Application{
		Origin:         "github.com/containerpak/bottles",
		Version:        "66.1",
		ParsedOverride: types.Override{Env: []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8"}},
	}
	directory := filepath.Join(home, ".config/cpak/overrides", "github.com", "containerpak", "bottles", app.Version)
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"env":["LANG=ja_JP.UTF-8","LC_ALL=ja_JP.UTF-8"]}`)
	if err := os.WriteFile(filepath.Join(directory, "cpak.json"), content, 0644); err != nil {
		t.Fatal(err)
	}
	if hostLocaleWins(app) {
		t.Fatal("a locale set by the user was replaced by the host one")
	}
}
