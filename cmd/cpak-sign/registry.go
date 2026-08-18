/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

const (
	manifestMediaType     = "application/vnd.oci.image.manifest.v1+json"
	indexMediaType        = "application/vnd.oci.image.index.v1+json"
	dockerManifestMedia   = "application/vnd.docker.distribution.manifest.v2+json"
	dockerIndexMedia      = "application/vnd.docker.distribution.manifest.list.v2+json"
	emptyConfigMediaType  = "application/vnd.oci.empty.v1+json"
	octetStreamMediaType  = "application/octet-stream"
	registryManifestLimit = 8 << 20
	registryTokenLimit    = 1 << 20
)

var manifestAccept = strings.Join([]string{manifestMediaType, indexMediaType, dockerManifestMedia, dockerIndexMedia}, ", ")

// emptyConfig is the blob an artifact manifest points its config at when it has
// no configuration of its own. The registry has to hold it like any other blob.
var emptyConfig = []byte("{}")

// descriptor is the subset of an OCI descriptor a referrer needs. pkg/oci has
// one too, and it carries a platform that would be encoded as an empty object
// into every manifest written here.
type descriptor struct {
	MediaType    string `json:"mediaType"`
	Digest       string `json:"digest"`
	Size         int64  `json:"size"`
	ArtifactType string `json:"artifactType,omitempty"`
}

// registry is the little of the OCI distribution API a publisher needs. cpak
// itself only ever reads from a registry, which is why pushing lives in the
// signing tool and not in pkg/oci.
type registry struct {
	reference     oci.Reference
	client        *http.Client
	authorization string
}

func newRegistry(reference oci.Reference) *registry {
	return &registry{reference: reference, client: &http.Client{Timeout: 2 * time.Minute}}
}

// host is where the API lives, which is not always where the reference points:
// the canonical name of Docker Hub is not the name of its registry.
func (r *registry) host() string {
	if r.reference.Registry == "index.docker.io" {
		return "registry-1.docker.io"
	}
	return r.reference.Registry
}

func (r *registry) endpoint(path string) string {
	scheme := "https"
	if loopbackHost(r.host()) {
		scheme = "http"
	}
	return scheme + "://" + r.host() + path
}

func (r *registry) repository(suffix string) string {
	return r.endpoint("/v2/" + r.reference.Repository + suffix)
}

// authorize takes one token for the whole push. The scope is asked for before
// anything is uploaded because a challenge answered halfway through would have
// to replay a request body that was already sent.
func (r *registry) authorize(ctx context.Context) error {
	response, err := r.send(ctx, http.MethodGet, r.endpoint("/v2/"), nil, nil)
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusUnauthorized {
		return nil
	}
	parsed, err := parseChallenge(response.Header.Get("WWW-Authenticate"))
	if err != nil {
		return err
	}
	credential := registryCredential()
	if credential.Username == "" && credential.Password == "" {
		return fmt.Errorf("%s asks for credentials: set %s and %s", r.reference.Registry, usernameVariable, passwordVariable)
	}
	if strings.EqualFold(parsed.scheme, "basic") {
		request, _ := http.NewRequest(http.MethodGet, "https://invalid", nil)
		request.SetBasicAuth(credential.Username, credential.Password)
		r.authorization = request.Header.Get("Authorization")
		return nil
	}
	if !strings.EqualFold(parsed.scheme, "bearer") {
		return fmt.Errorf("unsupported registry authentication scheme %q", parsed.scheme)
	}
	token, err := r.exchangeToken(ctx, parsed, credential)
	if err != nil {
		return err
	}
	r.authorization = "Bearer " + token
	return nil
}

func (r *registry) exchangeToken(ctx context.Context, parsed challenge, credential oci.Credential) (string, error) {
	endpoint, err := url.Parse(parsed.tokenURL)
	if err != nil || endpoint.Host == "" {
		return "", errors.New("the registry named an invalid token endpoint")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopbackHost(endpoint.Host)) {
		return "", errors.New("the registry token endpoint must be HTTPS")
	}
	// The credentials are the publisher's account. They go to the host the
	// image is being pushed to and to no other host the challenge names.
	if !sameHost(endpoint.Host, r.host()) {
		return "", fmt.Errorf("%s points its token endpoint at %s, which is not the registry being pushed to", r.reference.Registry, endpoint.Host)
	}
	query := endpoint.Query()
	if parsed.service != "" {
		query.Set("service", parsed.service)
	}
	query.Set("scope", "repository:"+r.reference.Repository+":pull,push")
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "cpak-sign")
	request.SetBasicAuth(credential.Username, credential.Password)
	response, err := r.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request a registry token: %w", err)
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return "", responseError("request a registry token", response)
	}
	body, err := readBounded(response.Body, registryTokenLimit)
	if err != nil {
		return "", fmt.Errorf("read the registry token: %w", err)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode the registry token: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", errors.New("the registry returned an empty token")
	}
	return payload.Token, nil
}

// subjectDescriptor reads the manifest a signature is attached to. It is read
// and not assumed: the digest has to exist in this repository, and it has to be
// the digest of what the registry serves, or the referrer would hang off
// nothing.
func (r *registry) subjectDescriptor(ctx context.Context, imageDigest string) (descriptor, error) {
	response, err := r.send(ctx, http.MethodGet, r.repository("/manifests/"+imageDigest), nil, map[string]string{"Accept": manifestAccept})
	if err != nil {
		return descriptor{}, err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return descriptor{}, responseError("read "+imageDigest, response)
	}
	body, err := readBounded(response.Body, registryManifestLimit)
	if err != nil {
		return descriptor{}, fmt.Errorf("read %s: %w", imageDigest, err)
	}
	if digestOf(body) != imageDigest {
		return descriptor{}, fmt.Errorf("%s served a manifest that is not %s", r.reference.Registry, imageDigest)
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		var header struct {
			MediaType string `json:"mediaType"`
		}
		if err = json.Unmarshal(body, &header); err != nil {
			return descriptor{}, fmt.Errorf("decode the media type of %s: %w", imageDigest, err)
		}
		mediaType = header.MediaType
	}
	if mediaType == "" {
		return descriptor{}, fmt.Errorf("%s declares no media type for %s", r.reference.Registry, imageDigest)
	}
	return descriptor{MediaType: mediaType, Digest: imageDigest, Size: int64(len(body))}, nil
}

func (r *registry) pushBlob(ctx context.Context, mediaType string, content []byte) (descriptor, error) {
	blob := descriptor{MediaType: mediaType, Digest: digestOf(content), Size: int64(len(content))}
	present, err := r.blobPresent(ctx, blob.Digest)
	if err != nil {
		return descriptor{}, err
	}
	if present {
		return blob, nil
	}
	response, err := r.send(ctx, http.MethodPost, r.repository("/blobs/uploads/"), nil, nil)
	if err != nil {
		return descriptor{}, err
	}
	location := response.Header.Get("Location")
	status := response.StatusCode
	closeResponse(response)
	if status != http.StatusAccepted {
		return descriptor{}, fmt.Errorf("start an upload to %s: %s", r.reference.ContextName(), http.StatusText(status))
	}
	target, err := r.uploadURL(location, blob.Digest)
	if err != nil {
		return descriptor{}, err
	}
	// A registry that hands the upload to storage of its own gets the bytes and
	// not the account they were authorized with.
	authorized := sameHost(target.Host, r.host())
	upload, err := r.sendTo(ctx, http.MethodPut, target.String(), content, map[string]string{"Content-Type": octetStreamMediaType}, authorized)
	if err != nil {
		return descriptor{}, err
	}
	defer closeResponse(upload)
	if upload.StatusCode != http.StatusCreated {
		return descriptor{}, responseError("upload "+blob.Digest, upload)
	}
	return blob, nil
}

func (r *registry) blobPresent(ctx context.Context, digest string) (bool, error) {
	response, err := r.send(ctx, http.MethodHead, r.repository("/blobs/"+digest), nil, nil)
	if err != nil {
		return false, err
	}
	defer closeResponse(response)
	if response.StatusCode == http.StatusOK {
		return true, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, responseError("look up "+digest, response)
}

// pushManifest writes the manifest under its own digest and answers with the
// subject the registry says it indexed it under, which is empty when the
// registry ignored the subject.
func (r *registry) pushManifest(ctx context.Context, content []byte) (string, error) {
	digest := digestOf(content)
	response, err := r.send(ctx, http.MethodPut, r.repository("/manifests/"+digest), content, map[string]string{"Content-Type": manifestMediaType})
	if err != nil {
		return "", err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusCreated {
		return "", responseError("push "+digest, response)
	}
	return response.Header.Get("OCI-Subject"), nil
}

func (r *registry) uploadURL(location, digest string) (*url.URL, error) {
	if location == "" {
		return nil, fmt.Errorf("%s accepted an upload without saying where to put it", r.reference.Registry)
	}
	base, err := url.Parse(r.endpoint("/"))
	if err != nil {
		return nil, err
	}
	target, err := base.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("the registry named an invalid upload location: %w", err)
	}
	if target.Scheme != "https" && !loopbackHost(target.Host) {
		return nil, fmt.Errorf("the registry pointed the upload at %s, which is not HTTPS", target)
	}
	query := target.Query()
	query.Set("digest", digest)
	target.RawQuery = query.Encode()
	return target, nil
}

func (r *registry) send(ctx context.Context, method, target string, body []byte, headers map[string]string) (*http.Response, error) {
	return r.sendTo(ctx, method, target, body, headers, true)
}

func (r *registry) sendTo(ctx context.Context, method, target string, body []byte, headers map[string]string, authorized bool) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "cpak-sign")
	if body != nil {
		request.ContentLength = int64(len(body))
	}
	if authorized && r.authorization != "" {
		request.Header.Set("Authorization", r.authorization)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("registry request: %w", err)
	}
	return response, nil
}

type challenge struct {
	scheme   string
	tokenURL string
	service  string
}

func parseChallenge(value string) (challenge, error) {
	scheme, parameters, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || scheme == "" {
		return challenge{}, errors.New("the registry returned an invalid authentication challenge")
	}
	parsed := challenge{scheme: scheme}
	for _, part := range splitParameters(parameters) {
		name, content, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		content = strings.Trim(strings.TrimSpace(content), `"`)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "realm":
			parsed.tokenURL = content
		case "service":
			parsed.service = content
		}
	}
	if strings.EqualFold(parsed.scheme, "bearer") && parsed.tokenURL == "" {
		return challenge{}, errors.New("the registry bearer challenge names no token endpoint")
	}
	return parsed, nil
}

func splitParameters(value string) []string {
	parts := make([]string, 0, 4)
	start := 0
	quoted := false
	for index, character := range value {
		if character == '"' {
			quoted = !quoted
		}
		if character == ',' && !quoted {
			parts = append(parts, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	return append(parts, strings.TrimSpace(value[start:]))
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the response is too large")
	}
	return body, nil
}

func responseError(action string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("%s: %s", action, message)
}

func closeResponse(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	_ = response.Body.Close()
}

func sameHost(candidate, registry string) bool {
	if strings.EqualFold(hostOnly(candidate), hostOnly(registry)) {
		return true
	}
	// Docker Hub answers for its own registry from a second host.
	return strings.EqualFold(hostOnly(registry), "registry-1.docker.io") && strings.EqualFold(hostOnly(candidate), "auth.docker.io")
}

func hostOnly(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func loopbackHost(value string) bool {
	host := hostOnly(value)
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
