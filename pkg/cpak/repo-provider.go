/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/registryauth"
)

// ErrLatestReleaseUnsupported is returned when the repository host does not
// offer a way to safely determine its latest release.
var ErrLatestReleaseUnsupported = errors.New("latest release lookup is not supported for this host")

// ErrDefaultBranchUnsupported is returned when the repository host does not
// expose its default branch through a supported API.
var ErrDefaultBranchUnsupported = errors.New("default branch lookup is not supported for this host")

type RepoProvider struct {
	Origin      string
	GitDir      string
	AccessToken string

	// Scheme is the protocol used to reach the remote host.
	Scheme string

	// APIBaseURL is the base URL of the host API used to resolve releases,
	// when empty it is derived from the origin host.
	APIBaseURL string

	// Client is the HTTP client used for every remote call.
	Client *http.Client
}

// NewRepoProvider creates a new RepoProvider instance. This is used to
// fetch files from a remote git repository. Note that we can't use the go-git
// library here, as we need to fetch files from a remote repository without
// cloning the entire repository. Imagine a repository with a single file
// that is 1GB in size, kek.
func NewRepoProvider(origin, gitDir string) (repoProvider *RepoProvider, err error) {
	origin, err = normalizeRepositoryOrigin(origin)
	if err != nil {
		return nil, err
	}
	GitDir, err := generateGitDir(origin, gitDir)
	if err != nil {
		return repoProvider, fmt.Errorf("failed to generate git path: %w", err)
	}

	return &RepoProvider{
		Origin: origin,
		GitDir: GitDir,
		Scheme: "https",
		Client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return nil
			},
		},
	}, nil
}

func (r *RepoProvider) scheme() string {
	if r.Scheme == "" {
		return "https"
	}
	return r.Scheme
}

func (r *RepoProvider) client() *http.Client {
	if r.Client == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return r.Client
}

func (r *RepoProvider) do(request *http.Request, authenticated bool) (*http.Response, error) {
	client := r.client()
	if !authenticated {
		return client.Do(request)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("repository access token is required")
	}
	request.Header.Set("Authorization", "Bearer "+r.AccessToken)
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return copy.Do(request)
}

func (r *RepoProvider) get(remoteURL string, authenticated bool, accept string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, remoteURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	return r.do(request, authenticated)
}

// GetLatestRelease returns the tag of the latest release published for the
// origin repository. Only GitHub is supported, any other host returns
// ErrLatestReleaseUnsupported so that the caller can report it instead of
// guessing a version.
func (r *RepoProvider) GetLatestRelease() (release string, err error) {
	url, err := r.latestReleaseURL()
	if err != nil {
		return "", err
	}

	resp, err := r.get(url, r.AccessToken != "", "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("failed to get latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get latest release: %s", resp.Status)
	}

	var payload struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode latest release: %w", err)
	}

	if payload.TagName == "" {
		return "", fmt.Errorf("latest release of %s has no tag name", r.Origin)
	}
	if payload.Draft || payload.Prerelease {
		return "", fmt.Errorf("latest release %s of %s is not a stable release", payload.TagName, r.Origin)
	}

	return payload.TagName, nil
}

// GetDefaultBranch returns the default branch declared by the repository host.
func (r *RepoProvider) GetDefaultBranch() (string, error) {
	url, err := r.repositoryURL()
	if err != nil {
		return "", err
	}
	resp, err := r.get(url, r.AccessToken != "", "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("failed to get repository: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get repository: %s", resp.Status)
	}
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode repository: %w", err)
	}
	if payload.DefaultBranch == "" {
		return "", fmt.Errorf("repository %s has no default branch", r.Origin)
	}
	return payload.DefaultBranch, nil
}

// GetAuthenticatedUser validates the configured GitHub token and returns its login.
func (r *RepoProvider) GetAuthenticatedUser() (string, error) {
	if r.AccessToken == "" {
		return "", fmt.Errorf("repository access token is required")
	}
	parts := strings.Split(r.Origin, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") {
		return "", fmt.Errorf("GitHub authentication is not supported for %s", r.Origin)
	}
	base := strings.TrimRight(r.APIBaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	resp, err := r.get(base+"/user", true, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("failed to get GitHub user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get GitHub user: %s", resp.Status)
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode GitHub user: %w", err)
	}
	if payload.Login == "" {
		return "", fmt.Errorf("GitHub user has no login")
	}
	return payload.Login, nil
}

func (r *RepoProvider) repositoryURL() (string, error) {
	parts := strings.Split(r.Origin, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid git url: %s", r.Origin)
	}
	base := strings.TrimRight(r.APIBaseURL, "/")
	if base == "" {
		if !strings.EqualFold(parts[0], "github.com") {
			return "", fmt.Errorf("%w: %s", ErrDefaultBranchUnsupported, parts[0])
		}
		base = "https://api.github.com"
	}
	return fmt.Sprintf("%s/repos/%s/%s", base, parts[1], parts[2]), nil
}

func (r *RepoProvider) latestReleaseURL() (url string, err error) {
	parts := strings.Split(r.Origin, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid git url: %s", r.Origin)
	}

	base := strings.TrimRight(r.APIBaseURL, "/")
	if base == "" {
		if !strings.EqualFold(parts[0], "github.com") {
			return "", fmt.Errorf("%w: %s", ErrLatestReleaseUnsupported, parts[0])
		}
		base = "https://api.github.com"
	}

	return fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, parts[1], parts[2]), nil
}

// generateGitDir generates the local path for the given git repository.
// Cache is stored in the following format (Go-like):
//
//	<cache-dir>/<host>/<user>/<repo>/<branch|release|commit>
func generateGitDir(gitURL string, gitDir string) (gitPath string, err error) {
	gitDir = strings.TrimRight(gitDir, "/")
	gitURL, err = normalizeRepositoryOrigin(gitURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(gitURL, "/")

	localPath := filepath.Join(append([]string{gitDir}, parts...)...)
	if err := os.MkdirAll(localPath, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create local path: %w", err)
	}

	return localPath, nil
}

func normalizeRepositoryOrigin(origin string) (string, error) {
	if origin != strings.TrimSpace(origin) {
		return "", fmt.Errorf("invalid repository origin %q", origin)
	}
	parts := strings.Split(origin, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid repository origin %q", origin)
	}
	host, err := normalizeRepositoryHost(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid repository origin %q", origin)
	}
	parts[0] = host
	for index, part := range parts {
		if index == 0 {
			continue
		}
		if part == "" || len(part) > 100 || part == "." || part == ".." || strings.Contains(part, "..") {
			return "", fmt.Errorf("invalid repository origin %q", origin)
		}
		if part != strings.ToLower(part) {
			return "", fmt.Errorf("repository path in %q must be lowercase", origin)
		}
		for _, character := range part {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
				continue
			}
			return "", fmt.Errorf("invalid repository origin %q", origin)
		}
	}
	return strings.Join(parts, "/"), nil
}

// NormalizeRepositoryOrigin canonicalizes only the case-insensitive host and
// validates the case-sensitive repository path without changing its target.
func NormalizeRepositoryOrigin(origin string) (string, error) {
	return normalizeRepositoryOrigin(origin)
}

func normalizeRepositoryHost(value string) (string, error) {
	value = strings.ToLower(value)
	host := value
	port := ""
	if strings.Contains(value, ":") {
		var err error
		host, port, err = net.SplitHostPort(value)
		if err != nil {
			return "", err
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid repository port")
		}
	}
	if len(host) == 0 || len(host) > 253 || strings.Contains(host, "..") {
		return "", errors.New("invalid repository host")
	}
	if net.ParseIP(host) == nil {
		if !strings.Contains(host, ".") {
			return "", errors.New("invalid repository host")
		}
		for _, character := range host {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '-' {
				continue
			}
			return "", errors.New("invalid repository host")
		}
	}
	if port != "" {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

// fetchFileContent fetches the content of a file from a remote URL and
// stores it in the given cache directory, returning the file content as
// a byte slice.
func (r *RepoProvider) fetchFileContent(rawURL, gitDir, name string, bypassCache, authenticated bool) (fileContent []byte, err error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse file URL: %w", err)
	}
	if bypassCache {
		query := parsedURL.Query()
		query.Set("cpak", strconv.FormatInt(time.Now().UnixNano(), 10))
		parsedURL.RawQuery = query.Encode()
	}
	request, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create file request: %w", err)
	}
	if bypassCache {
		request.Header.Set("Cache-Control", "no-cache")
		request.Header.Set("Pragma", "no-cache")
	}
	if authenticated {
		request.Header.Set("Accept", "application/vnd.github.raw+json")
	}

	resp, err := r.do(request, authenticated)
	if err != nil {
		return nil, fmt.Errorf("failed to get file content: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get file content: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// The name is the caller's, never the server's. It used to be taken from
	// the URL that had just been fetched, and a reference carrying a question
	// mark split into a query for url.Parse while filepath.Join went on
	// normalising the rest, so a publisher chose both where the file landed and
	// what it was called.
	filePath, err := containedPath(gitDir, name)
	if err != nil {
		return nil, err
	}
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	_, err = file.Write(body)
	if err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return body, nil
}

// getFileInDirectory fetches a file from a remote git repository, in the
// given directory. The directory can be either a branch, a release, or a
// commit.
// I am not really happy with this implementation, but it works for now.
func (r *RepoProvider) getFileInDirectory(filePath, reference, kind string, bypassCache bool) (fileContent []byte, err error) {
	if err = validateGitReference(reference); err != nil {
		return nil, err
	}
	name, err := singlePathComponent(filePath)
	if err != nil {
		return nil, err
	}

	// Generate URLs for the file in both GitHub and GitLab formats
	githubURL := fmt.Sprintf("%s://%s/raw/%s/%s", r.scheme(), r.Origin, reference, filePath)
	gitlabURL := fmt.Sprintf("%s://%s/-/raw/%s/%s", r.scheme(), r.Origin, reference, filePath)

	// The reference is escaped rather than used as it stands. A name git accepts
	// is not automatically a safe path component, and the manifest cache is not
	// the place to find that out.
	dirPath, err := containedPath(r.GitDir, filepath.Join(kind, url.PathEscape(reference)))
	if err != nil {
		return nil, err
	}
	err = os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}
	parts := strings.Split(r.Origin, "/")
	if r.AccessToken != "" && len(parts) == 3 && strings.EqualFold(parts[0], "github.com") {
		remoteURL, urlErr := r.githubFileURL(filePath, reference)
		if urlErr != nil {
			return nil, urlErr
		}
		return r.fetchFileContent(remoteURL, dirPath, name, bypassCache, true)
	}

	// Try to fetch the file content from GitHub first
	fileContent, err = r.fetchFileContent(githubURL, dirPath, name, bypassCache, false)
	if err == nil {
		return fileContent, nil
	}

	// If fetching from GitHub fails, try GitLab
	fileContent, err = r.fetchFileContent(gitlabURL, dirPath, name, bypassCache, false)
	if err == nil {
		return fileContent, nil
	}

	return nil, err
}

func (r *RepoProvider) githubFileURL(filePath, reference string) (string, error) {
	repositoryURL, err := r.repositoryURL()
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(repositoryURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/contents/" + url.PathEscape(filePath)
	query := parsed.Query()
	query.Set("ref", reference)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// GetFileInBranch is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given branch.
func (r *RepoProvider) GetFileInBranch(filePath, branch string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, branch, "branches", true)
}

// GetFileInRelease is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given release.
func (r *RepoProvider) GetFileInRelease(filePath, release string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, release, "releases", false)
}

// GetFileInCommit is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given commit.
func (r *RepoProvider) GetFileInCommit(filePath, commit string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, commit, "commits", false)
}

// GetLatestRelease returns the tag of the latest release published for the
// given origin.
func (c *Cpak) GetLatestRelease(origin string) (release string, err error) {
	repoProvider, err := c.newRepoProvider(origin)
	if err != nil {
		return "", fmt.Errorf("failed to create repo provider: %w", err)
	}
	return repoProvider.GetLatestRelease()
}

// GetDefaultBranch returns the default branch for an origin.
func (c *Cpak) GetDefaultBranch(origin string) (string, error) {
	repoProvider, err := c.newRepoProvider(origin)
	if err != nil {
		return "", err
	}
	branch, err := repoProvider.GetDefaultBranch()
	if errors.Is(err, ErrDefaultBranchUnsupported) {
		return "main", nil
	}
	return branch, err
}

func (c *Cpak) newRepoProvider(origin string) (*RepoProvider, error) {
	repoProvider, err := NewRepoProvider(origin, c.Options.ManifestsPath)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(repoProvider.Origin, "/")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid git url: %s", repoProvider.Origin)
	}
	token, err := registryauth.SourceCredential(c.Ctx, c.Options.RegistryAuthPath, repoProvider.Origin, parts[0])
	if err != nil {
		return nil, fmt.Errorf("load repository access for %s: %w", repoProvider.Origin, err)
	}
	repoProvider.AccessToken = token
	return repoProvider, nil
}
