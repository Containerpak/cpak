/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package oci

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mediaOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	mediaOCIIndex           = "application/vnd.oci.image.index.v1+json"
	mediaDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

var manifestAccept = strings.Join([]string{mediaOCIManifest, mediaOCIIndex, mediaDockerManifest, mediaDockerManifestList}, ", ")

// ErrAuthenticationRequired indicates that a registry rejected an anonymous request.
var ErrAuthenticationRequired = errors.New("oci: registry authentication is required")

// Credential contains credentials explicitly selected for one registry request.
type Credential struct {
	Username    string
	Password    string
	AccessToken string
	TokenHosts  []string
}

// CredentialProvider resolves explicit credentials for a registry request.
type CredentialProvider interface {
	Credential(context.Context, Reference) (Credential, error)
}

// Client pulls images through the OCI Distribution API.
type Client struct {
	HTTP        *http.Client
	Credentials CredentialProvider

	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

// Descriptor identifies content in an OCI registry.
type Descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    Platform          `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Platform identifies the system supported by an image manifest.
type Platform struct {
	Architecture string `json:"architecture,omitempty"`
	OS           string `json:"os,omitempty"`
	Variant      string `json:"variant,omitempty"`
}

// ConfigFile contains the OCI image configuration used by the runtime.
type ConfigFile struct {
	Architecture string      `json:"architecture,omitempty"`
	OS           string      `json:"os,omitempty"`
	Config       ImageConfig `json:"config"`
}

// ImageConfig contains process defaults from an OCI image.
type ImageConfig struct {
	Env        []string `json:"Env,omitempty"`
	Entrypoint []string `json:"Entrypoint,omitempty"`
	Cmd        []string `json:"Cmd,omitempty"`
	WorkingDir string   `json:"WorkingDir,omitempty"`
	User       string   `json:"User,omitempty"`
}

// Image is a resolved OCI image and its verified configuration.
type Image struct {
	Reference Reference
	Digest    string
	Config    []byte
	Layers    []Descriptor
}

type imageManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
	Manifests     []Descriptor `json:"manifests"`
}

// Resolve resolves a tag or digest to an OCI image.
func (c *Client) Resolve(ctx context.Context, value string) (Image, error) {
	ref, err := ParseReference(value)
	if err != nil {
		return Image{}, err
	}
	body, digest, mediaType, err := c.fetchManifest(ctx, ref, ref.Identifier)
	if err != nil {
		return Image{}, err
	}
	if mediaType == mediaOCIIndex || mediaType == mediaDockerManifestList {
		var index imageManifest
		if err = json.Unmarshal(body, &index); err != nil {
			return Image{}, fmt.Errorf("oci: decode image index: %w", err)
		}
		descriptor, found := platformManifest(index.Manifests)
		if !found {
			return Image{}, fmt.Errorf("oci: image has no linux/%s manifest", runtime.GOARCH)
		}
		body, digest, mediaType, err = c.fetchManifest(ctx, ref, descriptor.Digest)
		if err != nil {
			return Image{}, err
		}
	}
	if mediaType != mediaOCIManifest && mediaType != mediaDockerManifest {
		return Image{}, fmt.Errorf("oci: unsupported manifest media type %q", mediaType)
	}
	var manifest imageManifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		return Image{}, fmt.Errorf("oci: decode image manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || !validDescriptor(manifest.Config) {
		return Image{}, fmt.Errorf("oci: invalid image manifest")
	}
	for _, layer := range manifest.Layers {
		if !validDescriptor(layer) {
			return Image{}, fmt.Errorf("oci: invalid layer descriptor")
		}
	}
	config, err := c.readBlob(ctx, ref, manifest.Config)
	if err != nil {
		return Image{}, fmt.Errorf("oci: read image config: %w", err)
	}
	return Image{Reference: ref, Digest: digest, Config: config, Layers: manifest.Layers}, nil
}

// Digest resolves an image reference to an immutable manifest digest.
func (c *Client) Digest(ctx context.Context, value string) (string, error) {
	image, err := c.Resolve(ctx, value)
	if err != nil {
		return "", err
	}
	return image.Digest, nil
}

// Blob opens a verified OCI blob response.
func (c *Client) Blob(ctx context.Context, ref Reference, descriptor Descriptor) (io.ReadCloser, error) {
	if !validDescriptor(descriptor) {
		return nil, fmt.Errorf("oci: invalid blob descriptor")
	}
	response, err := c.request(ctx, ref, http.MethodGet, "/v2/"+ref.Repository+"/blobs/"+descriptor.Digest, "")
	if err != nil {
		return nil, err
	}
	if redirectStatus(response.StatusCode) {
		redirected, redirectErr := c.followBlobRedirect(ctx, ref, response, "")
		response.Body.Close()
		if redirectErr != nil {
			return nil, redirectErr
		}
		response = redirected
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		return nil, responseError("read blob", response)
	}
	return response.Body, nil
}

func (c *Client) BlobRange(ctx context.Context, ref Reference, descriptor Descriptor, offset, length int64) (io.ReadCloser, error) {
	if !validDescriptor(descriptor) || offset < 0 || length <= 0 || offset > descriptor.Size-length {
		return nil, fmt.Errorf("oci: invalid blob range")
	}
	rangeValue := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	response, err := c.requestWithHeaders(ctx, ref, http.MethodGet, "/v2/"+ref.Repository+"/blobs/"+descriptor.Digest, "", map[string]string{"Range": rangeValue})
	if err != nil {
		return nil, err
	}
	if redirectStatus(response.StatusCode) {
		redirected, redirectErr := c.followBlobRedirect(ctx, ref, response, rangeValue)
		response.Body.Close()
		if redirectErr != nil {
			return nil, redirectErr
		}
		response = redirected
	}
	if response.StatusCode != http.StatusPartialContent {
		defer response.Body.Close()
		return nil, responseError("read blob range", response)
	}
	expected := fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, descriptor.Size)
	if response.Header.Get("Content-Range") != expected || response.ContentLength != -1 && response.ContentLength != length {
		response.Body.Close()
		return nil, fmt.Errorf("oci: invalid blob range response")
	}
	return response.Body, nil
}

func (c *Client) followBlobRedirect(ctx context.Context, ref Reference, response *http.Response, rangeValues ...string) (*http.Response, error) {
	rangeValue := ""
	if len(rangeValues) > 0 {
		rangeValue = rangeValues[0]
	}
	target, err := response.Location()
	if err != nil {
		return nil, fmt.Errorf("oci: invalid blob redirect: %w", err)
	}
	if err = validateBlobRedirect(ref, target); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "cpak")
	if rangeValue != "" {
		request.Header.Set("Range", rangeValue)
	}
	client := *c.client()
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("oci: too many blob redirects")
		}
		if err := validateBlobRedirect(ref, next.URL); err != nil {
			return err
		}
		next.Header.Del("Authorization")
		return nil
	}
	redirected, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("oci: follow blob redirect: %w", err)
	}
	return redirected, nil
}

func validateBlobRedirect(ref Reference, target *url.URL) error {
	if target == nil || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return errors.New("oci: invalid blob redirect")
	}
	if target.Scheme != "https" {
		if target.Scheme != "http" || !loopbackRegistry(ref.Registry) || !loopbackRegistry(target.Host) {
			return errors.New("oci: blob redirect requires HTTPS")
		}
	}
	if unsafeEndpoint(target.Hostname()) && !loopbackRegistry(ref.Registry) {
		return errors.New("oci: blob redirect targets a private address")
	}
	return nil
}

func redirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func (c *Client) fetchManifest(ctx context.Context, ref Reference, identifier string) ([]byte, string, string, error) {
	response, err := c.request(ctx, ref, http.MethodGet, "/v2/"+ref.Repository+"/manifests/"+identifier, manifestAccept)
	if err != nil {
		return nil, "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", "", responseError("read manifest", response)
	}
	body, err := readBoundedResponse(response.Body, 8<<20, "manifest")
	if err != nil {
		return nil, "", "", err
	}
	digest := digestBytes(body)
	declared := response.Header.Get("Docker-Content-Digest")
	if declared != "" && declared != digest {
		return nil, "", "", fmt.Errorf("oci: manifest digest mismatch")
	}
	if strings.HasPrefix(identifier, "sha256:") && identifier != digest {
		return nil, "", "", fmt.Errorf("oci: manifest digest mismatch")
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		var header struct {
			MediaType string `json:"mediaType"`
		}
		if err = json.Unmarshal(body, &header); err != nil {
			return nil, "", "", fmt.Errorf("oci: decode manifest media type: %w", err)
		}
		mediaType = header.MediaType
	}
	return body, digest, mediaType, nil
}

func (c *Client) readBlob(ctx context.Context, ref Reference, descriptor Descriptor) ([]byte, error) {
	reader, err := c.Blob(ctx, ref, descriptor)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if descriptor.Size > 8<<20 {
		return nil, fmt.Errorf("oci: image config is too large")
	}
	body, err := io.ReadAll(io.LimitReader(reader, descriptor.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != descriptor.Size || digestBytes(body) != descriptor.Digest {
		return nil, fmt.Errorf("oci: blob digest mismatch")
	}
	return body, nil
}

func (c *Client) request(ctx context.Context, ref Reference, method, path, accept string) (*http.Response, error) {
	return c.requestWithHeaders(ctx, ref, method, path, accept, nil)
}

func (c *Client) requestWithHeaders(ctx context.Context, ref Reference, method, path, accept string, headers map[string]string) (*http.Response, error) {
	request, err := c.newRequest(ctx, ref, method, path, accept)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.registryClient(ref).Do(request)
	if err != nil {
		return nil, fmt.Errorf("oci: registry request: %w", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	challenge, err := parseChallenge(response.Header.Get("WWW-Authenticate"))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	credential, err := c.credential(ctx, ref)
	if err != nil {
		return nil, err
	}
	authorization, err := c.authorization(ctx, ref, challenge, credential)
	if err != nil {
		return nil, err
	}
	retry, err := c.newRequest(ctx, ref, method, path, accept)
	if err != nil {
		return nil, err
	}
	retry.Header.Set("Authorization", authorization)
	for name, value := range headers {
		retry.Header.Set(name, value)
	}
	return c.registryClient(ref).Do(retry)
}

func (c *Client) newRequest(ctx context.Context, ref Reference, method, path, accept string) (*http.Request, error) {
	host := ref.Registry
	if host == "index.docker.io" {
		host = "registry-1.docker.io"
	}
	scheme := "https"
	if loopbackRegistry(host) {
		scheme = "http"
	}
	request, err := http.NewRequestWithContext(ctx, method, scheme+"://"+host+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "cpak")
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	return request, nil
}

func (c *Client) authorization(ctx context.Context, ref Reference, challenge authChallenge, credential Credential) (string, error) {
	if strings.EqualFold(challenge.Scheme, "basic") {
		if credential.Username == "" && credential.Password == "" {
			return "", ErrAuthenticationRequired
		}
		request, _ := http.NewRequest(http.MethodGet, "https://invalid", nil)
		request.SetBasicAuth(credential.Username, credential.Password)
		return request.Header.Get("Authorization"), nil
	}
	if !strings.EqualFold(challenge.Scheme, "bearer") {
		return "", fmt.Errorf("oci: unsupported registry authentication scheme %q", challenge.Scheme)
	}
	if credential.AccessToken != "" {
		return "Bearer " + credential.AccessToken, nil
	}
	key := ref.Registry + "\x00" + challenge.Service + "\x00" + challenge.Scope
	if token := c.cachedToken(key); token != "" {
		return "Bearer " + token, nil
	}
	token, expiry, err := c.exchangeToken(ctx, ref, challenge, credential)
	if err != nil {
		return "", err
	}
	c.cacheToken(key, token, expiry)
	return "Bearer " + token, nil
}

func (c *Client) exchangeToken(ctx context.Context, ref Reference, challenge authChallenge, credential Credential) (string, time.Time, error) {
	endpoint, err := url.Parse(challenge.TokenURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		if err == nil && endpoint != nil && endpoint.Scheme == "http" && loopbackRegistry(endpoint.Host) {
			// Local registries are allowed for development and tests.
		} else {
			return "", time.Time{}, fmt.Errorf("oci: invalid registry token endpoint")
		}
	}
	if unsafeEndpoint(endpoint.Hostname()) && !loopbackRegistry(ref.Registry) {
		return "", time.Time{}, fmt.Errorf("oci: registry token endpoint is not public")
	}
	if credential.Username != "" || credential.Password != "" {
		if !allowedTokenHost(endpoint.Hostname(), ref.Registry, credential.TokenHosts) {
			return "", time.Time{}, fmt.Errorf("oci: token endpoint is not approved for this registry")
		}
	}
	query := endpoint.Query()
	if challenge.Service != "" {
		query.Set("service", challenge.Service)
	}
	if challenge.Scope != "" {
		query.Set("scope", challenge.Scope)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	if credential.Username != "" || credential.Password != "" {
		request.SetBasicAuth(credential.Username, credential.Password)
	}
	client := *c.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("oci: request registry token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return "", time.Time{}, ErrAuthenticationRequired
		}
		return "", time.Time{}, responseError("request registry token", response)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	body, err := readBoundedResponse(response.Body, 1<<20, "registry token")
	if err != nil {
		return "", time.Time{}, err
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return "", time.Time{}, fmt.Errorf("oci: decode registry token: %w", err)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", time.Time{}, fmt.Errorf("oci: registry returned an empty token")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 60
	}
	return payload.Token, time.Now().Add(time.Duration(expiresIn) * time.Second), nil
}

func readBoundedResponse(reader io.Reader, limit int64, name string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("oci: read %s: %w", name, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("oci: %s is too large", name)
	}
	return body, nil
}

func (c *Client) credential(ctx context.Context, ref Reference) (Credential, error) {
	if c.Credentials == nil {
		return Credential{}, nil
	}
	return c.Credentials.Credential(ctx, ref)
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (c *Client) registryClient(ref Reference) *http.Client {
	client := *c.client()
	expected := ref.Registry
	if expected == "index.docker.io" {
		expected = "registry-1.docker.io"
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !strings.EqualFold(request.URL.Host, expected) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &client
}

func (c *Client) cachedToken(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.tokens[key]
	if entry.value == "" || time.Until(entry.expiresAt) < 10*time.Second {
		delete(c.tokens, key)
		return ""
	}
	return entry.value
}

func (c *Client) cacheToken(key, value string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = make(map[string]cachedToken)
	}
	c.tokens[key] = cachedToken{value: value, expiresAt: expiresAt}
}

func platformManifest(manifests []Descriptor) (Descriptor, bool) {
	variant := ""
	if runtime.GOARCH == "arm" {
		variant = "v" + strconv.Itoa(7)
	}
	for _, descriptor := range manifests {
		if descriptor.Platform.OS == "linux" && descriptor.Platform.Architecture == runtime.GOARCH && (variant == "" || descriptor.Platform.Variant == "" || descriptor.Platform.Variant == variant) {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func validDescriptor(descriptor Descriptor) bool {
	return descriptor.Size >= 0 && digestPattern.MatchString(descriptor.Digest)
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func loopbackRegistry(host string) bool {
	hostname := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsed
	}
	return hostname == "localhost" || net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback()
}

func unsafeEndpoint(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}

func allowedTokenHost(host, registry string, allowed []string) bool {
	registryHost := registry
	if parsed, _, err := net.SplitHostPort(registry); err == nil {
		registryHost = parsed
	}
	if strings.EqualFold(host, registryHost) || registry == "index.docker.io" && host == "auth.docker.io" {
		return true
	}
	for _, candidate := range allowed {
		if strings.EqualFold(host, candidate) {
			return true
		}
	}
	return false
}

func responseError(action string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("oci: %s: %s", action, message)
}

type authChallenge struct {
	Scheme   string
	TokenURL string
	Service  string
	Scope    string
}

func parseChallenge(value string) (authChallenge, error) {
	scheme, parameters, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || scheme == "" {
		return authChallenge{}, fmt.Errorf("oci: registry returned an invalid authentication challenge")
	}
	challenge := authChallenge{Scheme: scheme}
	for _, part := range splitChallengeParameters(parameters) {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "realm":
			challenge.TokenURL = value
		case "service":
			challenge.Service = value
		case "scope":
			challenge.Scope = value
		}
	}
	if strings.EqualFold(challenge.Scheme, "bearer") && challenge.TokenURL == "" {
		return authChallenge{}, fmt.Errorf("oci: registry bearer challenge has no token endpoint")
	}
	return challenge, nil
}

func splitChallengeParameters(value string) []string {
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
