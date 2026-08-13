/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadBoundedResponseRejectsExtraContent(t *testing.T) {
	if _, err := readBoundedResponse(bytes.NewBufferString("12345"), 4, "test response"); err == nil {
		t.Fatal("oversized response was accepted")
	}
	body, err := readBoundedResponse(bytes.NewBufferString("1234"), 4, "test response")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "1234" {
		t.Fatalf("response body: %q", body)
	}
}

func TestClientResolvesImageAndVerifiesBlob(t *testing.T) {
	config := []byte(`{"architecture":"amd64","os":"linux","config":{"Env":["PATH=/usr/bin"]}}`)
	configDigest := digestBytes(config)
	manifest := imageManifest{
		SchemaVersion: 2,
		MediaType:     mediaOCIManifest,
		Config:        Descriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: int64(len(config))},
		Layers:        []Descriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:" + strings.Repeat("b", 64), Size: 12}},
	}
	manifestBody, _ := json.Marshal(manifest)
	manifestDigest := digestBytes(manifestBody)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/example/app/manifests/latest":
			writer.Header().Set("Content-Type", mediaOCIManifest)
			writer.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = writer.Write(manifestBody)
		case "/v2/example/app/blobs/" + configDigest:
			_, _ = writer.Write(config)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	image, err := (&Client{}).Resolve(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/example/app")
	if err != nil {
		t.Fatal(err)
	}
	if image.Digest != manifestDigest || len(image.Layers) != 1 || string(image.Config) != string(config) {
		t.Fatalf("unexpected resolved image: %+v", image)
	}
}

func TestClientRejectsConfigDigestMismatch(t *testing.T) {
	expected := []byte("expected")
	actual := []byte("modified")
	configDigest := digestBytes(expected)
	manifestBody := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"digest":%q,"size":%d},"layers":[]}`, mediaOCIManifest, configDigest, len(actual)))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v2/example/app/manifests/latest":
			writer.Header().Set("Content-Type", mediaOCIManifest)
			_, _ = writer.Write(manifestBody)
		case "/v2/example/app/blobs/" + configDigest:
			_, _ = writer.Write(actual)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	if _, err := (&Client{}).Resolve(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/example/app"); err == nil || !strings.Contains(err.Error(), "blob digest mismatch") {
		t.Fatalf("modified config was accepted: %v", err)
	}
}

func TestClientUsesBearerChallengeWithoutAmbientCredentials(t *testing.T) {
	config := []byte(`{"config":{}}`)
	configDigest := digestBytes(config)
	manifestBody := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"digest":%q,"size":%d},"layers":[]}`, mediaOCIManifest, configDigest, len(config)))
	var tokenRequests atomic.Int32
	var authorization string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/token":
			tokenRequests.Add(1)
			_, _ = writer.Write([]byte(`{"token":"short-lived","expires_in":60}`))
		case request.URL.Path == "/v2/example/app/manifests/latest" && request.Header.Get("Authorization") == "":
			writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="test",scope="repository:example/app:pull"`, server.URL+"/token"))
			writer.WriteHeader(http.StatusUnauthorized)
		case request.URL.Path == "/v2/example/app/manifests/latest":
			authorization = request.Header.Get("Authorization")
			writer.Header().Set("Content-Type", mediaOCIManifest)
			_, _ = writer.Write(manifestBody)
		case request.URL.Path == "/v2/example/app/blobs/"+configDigest:
			_, _ = writer.Write(config)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	_, err := (&Client{}).Resolve(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/example/app")
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer short-lived" || tokenRequests.Load() != 1 {
		t.Fatalf("unexpected authentication: header=%q token requests=%d", authorization, tokenRequests.Load())
	}
}

func TestClientRejectsCredentialRedirect(t *testing.T) {
	var received atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		received.Store(true)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			http.Redirect(writer, request, target.URL, http.StatusFound)
			return
		}
		writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="test",scope="repository:example/app:pull"`, sourceURL(request)+"/token"))
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer source.Close()

	provider := staticCredentials{credential: Credential{Username: "user", Password: "secret"}}
	_, err := (&Client{Credentials: provider}).Resolve(context.Background(), strings.TrimPrefix(source.URL, "http://")+"/example/app")
	if err == nil {
		t.Fatal("credential redirect was accepted")
	}
	if received.Load() {
		t.Fatal("credential request followed the redirect")
	}
}

func TestClientRequiresExplicitTokenHost(t *testing.T) {
	ref, _ := ParseReference("ghcr.io/example/app")
	challenge := authChallenge{Scheme: "Bearer", TokenURL: "https://auth.example.com/token"}
	var called atomic.Bool
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called.Store(true)
		username, password, ok := request.BasicAuth()
		if !ok || username != "user" || password != "secret" {
			t.Fatalf("unexpected token credentials: %q %q %v", username, password, ok)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"token":"approved","expires_in":60}`)), Header: make(http.Header)}, nil
	})}}
	credential := Credential{Username: "user", Password: "secret"}
	if _, _, err := client.exchangeToken(context.Background(), ref, challenge, credential); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("unapproved token host was accepted: %v", err)
	}
	if called.Load() {
		t.Fatal("unapproved token host received a request")
	}

	credential.TokenHosts = []string{"auth.example.com"}
	token, _, err := client.exchangeToken(context.Background(), ref, challenge, credential)
	if err != nil || token != "approved" || !called.Load() {
		t.Fatalf("approved token host failed: token=%q called=%v err=%v", token, called.Load(), err)
	}
}

func TestBlobFollowsRedirectWithoutRegistryCredential(t *testing.T) {
	content := []byte("layer")
	descriptor := Descriptor{Digest: digestBytes(content), Size: int64(len(content))}
	var authorization string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		_, _ = writer.Write(content)
	}))
	defer target.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "" {
			writer.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(writer, request, target.URL+"/layer", http.StatusTemporaryRedirect)
	}))
	defer registry.Close()
	ref, _ := ParseReference(strings.TrimPrefix(registry.URL, "http://") + "/example/app")
	provider := staticCredentials{credential: Credential{Username: "user", Password: "secret"}}
	reader, err := (&Client{Credentials: provider}).Blob(context.Background(), ref, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if authorization != "" {
		t.Fatal("registry credential was forwarded to blob storage")
	}
}

func TestBlobRejectsPrivateRedirectFromPublicRegistry(t *testing.T) {
	ref, _ := ParseReference("registry.example.com/example/app")
	request := httptest.NewRequest(http.MethodGet, "https://registry.example.com/v2/example/app/blobs/test", nil)
	response := &http.Response{
		StatusCode: http.StatusTemporaryRedirect,
		Header:     http.Header{"Location": []string{"http://127.0.0.1/layer"}},
		Body:       http.NoBody,
		Request:    request,
	}
	if _, err := (&Client{}).followBlobRedirect(context.Background(), ref, response); err == nil {
		t.Fatal("private blob redirect was accepted")
	}
}

func TestBlobRangeRequiresExactPartialResponse(t *testing.T) {
	content := []byte("0123456789")
	descriptor := Descriptor{Digest: digestBytes(content), Size: int64(len(content))}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=2-5" {
			t.Fatalf("range: %q", request.Header.Get("Range"))
		}
		writer.Header().Set("Content-Range", "bytes 2-5/10")
		writer.Header().Set("Content-Length", "4")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(content[2:6])
	}))
	defer server.Close()
	ref, _ := ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	reader, err := (&Client{HTTP: server.Client()}).BlobRange(context.Background(), ref, descriptor, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2345" {
		t.Fatalf("content: %q", got)
	}
}

func TestBlobRangeRejectsFullResponse(t *testing.T) {
	content := []byte("0123456789")
	descriptor := Descriptor{Digest: digestBytes(content), Size: int64(len(content))}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()
	ref, _ := ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if _, err := (&Client{HTTP: server.Client()}).BlobRange(context.Background(), ref, descriptor, 2, 4); err == nil {
		t.Fatal("full response was accepted for a range request")
	}
}

func TestBlobRangePreservesRangeAcrossRedirect(t *testing.T) {
	content := []byte("0123456789")
	descriptor := Descriptor{Digest: digestBytes(content), Size: int64(len(content))}
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Header.Get("Range")
		writer.Header().Set("Content-Range", "bytes 2-5/10")
		writer.Header().Set("Content-Length", "4")
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(content[2:6])
	}))
	defer target.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/layer", http.StatusTemporaryRedirect)
	}))
	defer registry.Close()
	ref, _ := ParseReference(strings.TrimPrefix(registry.URL, "http://") + "/example/app")
	reader, err := (&Client{}).BlobRange(context.Background(), ref, descriptor, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if received != "bytes=2-5" {
		t.Fatalf("redirected range: %q", received)
	}
}

func sourceURL(request *http.Request) string {
	return "http://" + request.Host
}

type staticCredentials struct {
	credential Credential
}

func (s staticCredentials) Credential(context.Context, Reference) (Credential, error) {
	return s.credential, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
