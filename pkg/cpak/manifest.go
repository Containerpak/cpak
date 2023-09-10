package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// ValidateManifest validates a manifest file, by ensuring all
// required fields are present.
func (c *Cpak) ValidateManifest(manifest *types.Manifest) (err error) {
	if manifest.Name == "" {
		return errors.New("name is mandatory and must be populated")
	}
	if manifest.Description == "" {
		return errors.New("description is mandatory and must be populated")
	}
	if manifest.Version == "" {
		return errors.New("version is mandatory and must be populated")
	}
	if manifest.Image == "" {
		return errors.New("image is mandatory and must be populated")
	}
	if len(manifest.Binaries) == 0 {
		return errors.New("binaries is mandatory and must be populated")
	}
	return nil
}

// fetchManifest fetches the manifest file from the given origin.
func (c *Cpak) FetchManifest(origin, branch, release, commit string) (manifest *types.Manifest, err error) {
	// remove trailing .git if present
	if origin[len(origin)-4:] == ".git" {
		origin = origin[:len(origin)-4]
	}

	// if any protocol is specified, we release a failuer since we force
	// the use of https and the user should learn about it
	if strings.Contains(origin, "://") {
		return nil, fmt.Errorf("do not specify any protocol in the origin repository URL")
	}

	repoProvider, err := NewRepoProvider(origin, c.Options.CachePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create repo provider: %w", err)
	}

	var manifestContent []byte
	switch {
	case branch != "":
		manifestContent, err = repoProvider.GetFileInBranch("cpak.json", branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	case release != "":
		manifestContent, err = repoProvider.GetFileInRelease("cpak.json", release)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	case commit != "":
		manifestContent, err = repoProvider.GetFileInCommit("cpak.json", commit)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	default:
		return nil, fmt.Errorf("no branch, release or commit specified")
	}

	manifest = &types.Manifest{}
	err = json.Unmarshal(manifestContent, manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest file: %w", err)
	}

	return manifest, nil
}
