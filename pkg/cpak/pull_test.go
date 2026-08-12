package cpak

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

func TestLayerAvailableRequiresExactDirectory(t *testing.T) {
	c := newTestCpak(t)
	if err := os.MkdirAll(c.GetInStoreDir("layers", "abc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.GetInStoreDir("layers", "abc.partial-old"), 0755); err != nil {
		t.Fatal(err)
	}
	available, err := c.layerAvailable("abc")
	if err != nil || !available {
		t.Fatalf("expected exact layer directory to be available: %v", err)
	}
	available, err = c.layerAvailable("ab")
	if err != nil || available {
		t.Fatalf("partial digest must not match: available=%v err=%v", available, err)
	}
	if _, err := os.Stat(filepath.Join(c.Options.StorePath, "layers", "abc.partial-old")); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackImageLayersReturnsExistingLayers(t *testing.T) {
	c := newTestCpak(t)
	layerID := "273e8f13c6afc8e63c46855824472f90b15bf43e14e84becbf5a114421b94008"
	layer := oci.Descriptor{Digest: "sha256:" + layerID, Size: 14}
	if err := os.MkdirAll(c.GetInStoreDir("layers", layerID), 0755); err != nil {
		t.Fatal(err)
	}

	layers, err := c.unpackImageLayers("test", nil, oci.Reference{}, []oci.Descriptor{layer})
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0] != layerID {
		t.Fatalf("existing layer missing from result: %v", layers)
	}
}

func TestPublishLayerAcceptsExistingLayer(t *testing.T) {
	c := newTestCpak(t)
	target := c.GetInStoreDir("layers", "abc")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	source, err := os.MkdirTemp(c.GetInStoreDir("layers"), "abc.partial-")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.publishLayer(source, "abc"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("temporary layer still exists: %v", err)
	}
}

func TestPublishLayerRejectsExistingFile(t *testing.T) {
	c := newTestCpak(t)
	target := c.GetInStoreDir("layers", "abc")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	source, err := os.MkdirTemp(c.GetInStoreDir("layers"), "abc.partial-")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.publishLayer(source, "abc"); err == nil {
		t.Fatal("expected an existing file to reject the layer")
	}
}

func TestDownloadLayerRejectsContentBeyondDeclaredSize(t *testing.T) {
	content := []byte("oversized")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()

	cp := newTestCpak(t)
	cp.Ctx = context.Background()
	cp.Options.CachePath = t.TempDir()
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	descriptor := oci.Descriptor{Digest: "sha256:" + digest, Size: 4}
	err = cp.downloadLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest)
	if err == nil || !strings.Contains(err.Error(), "received more than 4") {
		t.Fatalf("oversized layer was accepted: %v", err)
	}
	entries, err := os.ReadDir(cp.Options.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial layer was retained: %v", entries)
	}
}

func TestPullUsesCredentialBoundToPackageOrigin(t *testing.T) {
	origin := "github.com/example/private"
	server, image := authenticatedRegistry(t, "user", "secret")
	defer server.Close()

	ref, err := oci.ParseReference(image)
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	auth := fmt.Sprintf(`{"records":[{"origin":%q,"registry":%q,"repository":%q,"username":"user","password":"secret"}]}`, origin, ref.Registry, ref.Repository)
	if err = os.WriteFile(authPath, []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_REGISTRY_AUTH_FILE", authPath)

	cp := newTestCpak(t)
	cp.Ctx = context.Background()
	_, config, digest, err := cp.pull(image, "private", origin)
	if err != nil {
		t.Fatal(err)
	}
	if config == "" || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("unexpected image result: config=%q digest=%q", config, digest)
	}
}

func TestPullDoesNotReuseCredentialForAnotherOrigin(t *testing.T) {
	origin := "github.com/example/private"
	server, image := authenticatedRegistry(t, "user", "secret")
	defer server.Close()

	ref, err := oci.ParseReference(image)
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	auth := fmt.Sprintf(`{"records":[{"origin":%q,"registry":%q,"repository":%q,"username":"user","password":"secret"}]}`, origin, ref.Registry, ref.Repository)
	if err = os.WriteFile(authPath, []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_REGISTRY_AUTH_FILE", authPath)

	cp := newTestCpak(t)
	cp.Ctx = context.Background()
	if _, _, _, err = cp.pull(image, "private", "github.com/example/other"); err == nil {
		t.Fatal("credential was reused for another package origin")
	}
}

func authenticatedRegistry(t *testing.T, username, password string) (*httptest.Server, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","config":{}}`)
	configHash := sha256.Sum256(config)
	configDigest := fmt.Sprintf("sha256:%x", configHash[:])
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, configDigest, len(config)))

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		actualUsername, actualPassword, authenticated := request.BasicAuth()
		if !authenticated || actualUsername != username || actualPassword != password {
			writer.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v2/example/private/manifests/latest":
			writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = writer.Write(manifest)
		case "/v2/example/private/blobs/" + configDigest:
			_, _ = writer.Write(config)
		default:
			http.NotFound(writer, request)
		}
	}))
	return server, strings.TrimPrefix(server.URL, "http://") + "/example/private:latest"
}
