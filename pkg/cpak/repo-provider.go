package cpak

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type RepoProvider struct {
	Origin      string
	GitCacheDir string
}

// NewRepoProvider creates a new RepoProvider instance. This is used to
// fetch files from a remote git repository. Note that we can't use the go-git
// library here, as we need to fetch files from a remote repository without
// cloning the entire repository. Imagine a repository with a single file
// that is 1GB in size, kek.
func NewRepoProvider(origin, cacheDir string) (repoProvider *RepoProvider, err error) {
	gitCacheDir, err := generateGitCacheDir(origin, cacheDir)
	if err != nil {
		return repoProvider, fmt.Errorf("failed to generate git path: %w", err)
	}

	return &RepoProvider{
		Origin:      origin,
		GitCacheDir: gitCacheDir,
	}, nil
}

// generateGitCacheDir generates the local path for the given git repository.
// Cache is stored in the following format (Go-like):
//
//	<cache-dir>/<host>/<user>/<repo>/<branch|release|commit>
func generateGitCacheDir(gitURL string, cacheDir string) (gitPath string, err error) {
	cacheDir = strings.TrimRight(cacheDir, "/")
	parts := strings.Split(gitURL, "/")

	if len(parts) != 3 {
		return "", fmt.Errorf("invalid git url: %s", gitURL)
	}

	localPath := filepath.Join(append([]string{cacheDir}, parts...)...)
	if err := os.MkdirAll(localPath, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create local path: %w", err)
	}

	return localPath, nil
}

// fetchFileContent fetches the content of a file from a remote URL and
// stores it in the given cache directory, returning the file content as
// a byte slice.
func (r *RepoProvider) fetchFileContent(url, cacheDir string) (fileContent []byte, err error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	resp, err := client.Get(url)
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

	filePath := filepath.Join(cacheDir, filepath.Base(url))
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
func (r *RepoProvider) getFileInDirectory(filePath, reference, cacheDir string) (fileContent []byte, err error) {
	// Generate URLs for the file in both GitHub and GitLab formats
	githubURL := fmt.Sprintf("https://%s/raw/%s/%s", r.Origin, reference, filePath)
	gitlabURL := fmt.Sprintf("https://%s/-/raw/%s/%s", r.Origin, reference, filePath)

	// Generate the local path for the given directory
	dirPath := filepath.Join(r.GitCacheDir, cacheDir)
	err = os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Try to fetch the file content from GitHub first
	fileContent, err = r.fetchFileContent(githubURL, dirPath)
	if err == nil {
		return fileContent, nil
	}

	// If fetching from GitHub fails, try GitLab
	fileContent, err = r.fetchFileContent(gitlabURL, dirPath)
	if err == nil {
		return fileContent, nil
	}

	return nil, err
}

// GetFileInBranch is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given branch.
func (r *RepoProvider) GetFileInBranch(filePath, branch string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, branch, filepath.Join("branches", branch))
}

// GetFileInRelease is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given release.
func (r *RepoProvider) GetFileInRelease(filePath, release string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, release, filepath.Join("releases", release))
}

// GetFileInCommit is a wrapper around getFileInDirectory, that fetches a file
// from a remote git repository, in the given commit.
func (r *RepoProvider) GetFileInCommit(filePath, commit string) (fileContent []byte, err error) {
	return r.getFileInDirectory(filePath, commit, filepath.Join("commits", commit))
}
