/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

const (
	fakeUsername    = "publisher"
	fakePassword    = "secret"
	fakeToken       = "fake-token"
	fakeRepository  = "example/app"
	configMediaType = "application/vnd.oci.image.config.v1+json"
)

// fakeRegistry is enough of the distribution API to resolve an image and to
// take a referrer. It records what was pushed so that a test can read the
// manifest the registry was actually given, instead of the one cpak-sign meant
// to give it.
type fakeRegistry struct {
	t              *testing.T
	server         *httptest.Server
	manifests      map[string][]byte
	mediaTypes     map[string]string
	blobs          map[string][]byte
	uploaded       map[string][]byte
	pushed         map[string][]byte
	subjects       map[string]string
	indexReferrers bool
	requireToken   bool
	requests       int
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()
	registry := &fakeRegistry{
		t:              t,
		manifests:      map[string][]byte{},
		mediaTypes:     map[string]string{},
		blobs:          map[string][]byte{},
		uploaded:       map[string][]byte{},
		pushed:         map[string][]byte{},
		subjects:       map[string]string{},
		indexReferrers: true,
	}
	registry.server = httptest.NewServer(registry)
	t.Cleanup(registry.server.Close)
	return registry
}

// reference is the image reference a test hands to cpak-sign.
func (f *fakeRegistry) reference(identifier string) string {
	host := strings.TrimPrefix(f.server.URL, "http://")
	if identifier == "" {
		return host + "/" + fakeRepository
	}
	separator := ":"
	if strings.HasPrefix(identifier, "sha256:") {
		separator = "@"
	}
	return host + "/" + fakeRepository + separator + identifier
}

// publishImage stores an index and the one manifest it names for this
// architecture, and answers with the digest of that manifest, which is the
// digest an installation on this machine measures.
func (f *fakeRegistry) publishImage(tag string) string {
	f.t.Helper()
	config := []byte(fmt.Sprintf(`{"architecture":%q,"os":"linux","config":{}}`, runtime.GOARCH))
	f.blobs[digestOf(config)] = config

	manifest := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":%q,"config":{"mediaType":%q,"digest":%q,"size":%d},"layers":[]}`,
		manifestMediaType, configMediaType, digestOf(config), len(config),
	))
	manifestDigest := digestOf(manifest)
	f.manifests[manifestDigest] = manifest
	f.mediaTypes[manifestDigest] = manifestMediaType

	index := []byte(fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":%q,"manifests":[{"mediaType":%q,"digest":%q,"size":%d,"platform":{"architecture":%q,"os":"linux"}}]}`,
		indexMediaType, manifestMediaType, manifestDigest, len(manifest), runtime.GOARCH,
	))
	f.manifests[tag] = index
	f.mediaTypes[tag] = indexMediaType
	f.manifests[digestOf(index)] = index
	f.mediaTypes[digestOf(index)] = indexMediaType
	return manifestDigest
}

func (f *fakeRegistry) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	f.requests++
	prefix := "/v2/" + fakeRepository + "/"
	switch {
	case request.URL.Path == "/v2/":
		f.servePing(writer, request)
	case request.URL.Path == "/token":
		f.serveToken(writer, request)
	case strings.HasPrefix(request.URL.Path, prefix+"manifests/"):
		f.serveManifest(writer, request, strings.TrimPrefix(request.URL.Path, prefix+"manifests/"))
	case request.URL.Path == prefix+"blobs/uploads/":
		f.serveUploadStart(writer, request)
	case strings.HasPrefix(request.URL.Path, prefix+"blobs/uploads/"):
		f.serveUpload(writer, request)
	case strings.HasPrefix(request.URL.Path, prefix+"blobs/"):
		f.serveBlob(writer, request, strings.TrimPrefix(request.URL.Path, prefix+"blobs/"))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeRegistry) servePing(writer http.ResponseWriter, request *http.Request) {
	if !f.requireToken {
		writer.WriteHeader(http.StatusOK)
		return
	}
	if request.Header.Get("Authorization") == "" {
		writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, f.server.URL))
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	writer.WriteHeader(http.StatusOK)
}

func (f *fakeRegistry) serveToken(writer http.ResponseWriter, request *http.Request) {
	username, password, ok := request.BasicAuth()
	if !ok || username != fakeUsername || password != fakePassword {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if scope := request.URL.Query().Get("scope"); scope != "repository:"+fakeRepository+":pull,push" {
		f.t.Errorf("the token was asked for with scope %q, which does not allow a push", scope)
	}
	writer.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(writer, `{"token":%q}`, fakeToken)
}

func (f *fakeRegistry) authorized(writer http.ResponseWriter, request *http.Request) bool {
	if !f.requireToken || request.Header.Get("Authorization") == "Bearer "+fakeToken {
		return true
	}
	writer.WriteHeader(http.StatusUnauthorized)
	return false
}

func (f *fakeRegistry) serveManifest(writer http.ResponseWriter, request *http.Request, identifier string) {
	if !f.authorized(writer, request) {
		return
	}
	if request.Method == http.MethodPut {
		f.storeManifest(writer, request, identifier)
		return
	}
	body, found := f.manifests[identifier]
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", f.mediaTypes[identifier])
	writer.Header().Set("Docker-Content-Digest", digestOf(body))
	writer.Write(body)
}

func (f *fakeRegistry) storeManifest(writer http.ResponseWriter, request *http.Request, identifier string) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Fatalf("reading the pushed manifest failed: %v", err)
	}
	if digestOf(body) != identifier {
		f.t.Errorf("the manifest was pushed as %s but is %s", identifier, digestOf(body))
	}
	f.pushed[identifier] = body
	f.manifests[identifier] = body
	f.mediaTypes[identifier] = request.Header.Get("Content-Type")

	var pushed struct {
		Subject descriptor `json:"subject"`
	}
	if err = json.Unmarshal(body, &pushed); err == nil && pushed.Subject.Digest != "" && f.indexReferrers {
		f.subjects[identifier] = pushed.Subject.Digest
		writer.Header().Set("OCI-Subject", pushed.Subject.Digest)
	}
	writer.WriteHeader(http.StatusCreated)
}

func (f *fakeRegistry) serveUploadStart(writer http.ResponseWriter, request *http.Request) {
	if !f.authorized(writer, request) {
		return
	}
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Location", "/v2/"+fakeRepository+"/blobs/uploads/session?state=open")
	writer.WriteHeader(http.StatusAccepted)
}

func (f *fakeRegistry) serveUpload(writer http.ResponseWriter, request *http.Request) {
	if !f.authorized(writer, request) {
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		f.t.Fatalf("reading an uploaded blob failed: %v", err)
	}
	digest := request.URL.Query().Get("digest")
	if digest != digestOf(body) {
		f.t.Errorf("the blob was uploaded as %s but is %s", digest, digestOf(body))
	}
	if request.URL.Query().Get("state") != "open" {
		f.t.Errorf("the upload session was lost: %s", request.URL.RawQuery)
	}
	f.uploaded[digest] = body
	f.blobs[digest] = body
	writer.WriteHeader(http.StatusCreated)
}

func (f *fakeRegistry) serveBlob(writer http.ResponseWriter, request *http.Request, digest string) {
	if !f.authorized(writer, request) {
		return
	}
	body, found := f.blobs[digest]
	if !found {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Method == http.MethodHead {
		writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
		writer.WriteHeader(http.StatusOK)
		return
	}
	writer.Write(body)
}
