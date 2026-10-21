/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

const (
	UserInstallation   = "user"
	SystemInstallation = "system"
)

// Installation is a Flatpak installation on the host, read from its files:
// cpak never runs Flatpak to learn what it installed.
type Installation struct {
	Kind string
	Path string
}

// HostInstallations answers with the per-user and the system installation,
// honouring the same variables Flatpak reads to move them.
func HostInstallations() []Installation {
	user := os.Getenv("FLATPAK_USER_DIR")
	if user == "" {
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataHome = filepath.Join(home, ".local", "share")
			}
		}
		if dataHome != "" {
			user = filepath.Join(dataHome, "flatpak")
		}
	}
	system := os.Getenv("FLATPAK_SYSTEM_DIR")
	if system == "" {
		system = "/var/lib/flatpak"
	}
	installations := []Installation{}
	if user != "" {
		installations = append(installations, Installation{Kind: UserInstallation, Path: user})
	}
	return append(installations, Installation{Kind: SystemInstallation, Path: system})
}

// InstalledApp is an application deployed in an installation.
type InstalledApp struct {
	Ref          Ref
	Origin       string
	Commit       string
	Installation Installation
}

// Apps answers with the applications deployed in the installation, one per
// id: the branch its current link points to. A missing installation holds
// none.
func (i Installation) Apps() ([]InstalledApp, error) {
	root := filepath.Join(i.Path, "app")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("flatpak: read %s installation: %w", i.Kind, err)
	}
	apps := []InstalledApp{}
	for _, entry := range entries {
		if !entry.IsDir() || ValidateID(entry.Name()) != nil {
			continue
		}
		app, err := i.app(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(first, second int) bool { return apps[first].Ref.ID < apps[second].Ref.ID })
	return apps, nil
}

// Deployed reports whether an application is still deployed in the
// installation.
func (i Installation) Deployed(id string) bool {
	_, err := os.Lstat(filepath.Join(i.Path, "app", id, "current"))
	return err == nil
}

func (i Installation) app(id string) (InstalledApp, error) {
	directory := filepath.Join(i.Path, "app", id)
	current, err := os.Readlink(filepath.Join(directory, "current"))
	if err != nil {
		return InstalledApp{}, err
	}
	ref, err := ParseRef("app/" + id + "/" + filepath.ToSlash(current))
	if err != nil {
		return InstalledApp{}, fmt.Errorf("flatpak: %s points at %q: %w", id, current, err)
	}
	commit, err := os.Readlink(filepath.Join(directory, ref.Arch, ref.Branch, "active"))
	if err != nil {
		return InstalledApp{}, err
	}
	data, err := os.ReadFile(filepath.Join(directory, ref.Arch, ref.Branch, commit, "deploy"))
	if err != nil {
		return InstalledApp{}, fmt.Errorf("flatpak: read the deployment of %s: %w", id, err)
	}
	origin, err := deployOrigin(data)
	if err != nil {
		return InstalledApp{}, fmt.Errorf("flatpak: the deployment of %s: %w", id, err)
	}
	return InstalledApp{Ref: ref, Origin: origin, Commit: commit, Installation: i}, nil
}

// deployOrigin reads the remote name from a deploy file: a serialised
// (origin, commit, subpaths, installed size, metadata) tuple.
func deployOrigin(data []byte) (string, error) {
	value, err := gvariant.Parse("(ssasta{sv})", data)
	if err != nil {
		return "", err
	}
	members, _ := value.([]any)
	if len(members) == 0 {
		return "", errors.New("the deploy file names no origin")
	}
	origin, _ := members[0].(string)
	if ValidateRemoteName(origin) != nil {
		return "", fmt.Errorf("the deploy file names the remote %q", origin)
	}
	return origin, nil
}

// RemoteURL answers with the address the installation reads a remote from.
func (i Installation) RemoteURL(name string) (string, error) {
	file, err := i.repoConfig()
	if err != nil {
		return "", err
	}
	group := `remote "` + name + `"`
	location := file.String(group, "url")
	if location == "" {
		return "", fmt.Errorf("flatpak: the %s installation has no remote named %s", i.Kind, name)
	}
	return location, nil
}

// RemoteKey answers with the keyring the installation trusts a remote
// through. A remote that has signature checks off has none to give, and cpak
// does not read it.
func (i Installation) RemoteKey(name string) ([]byte, error) {
	file, err := i.repoConfig()
	if err != nil {
		return nil, err
	}
	group := `remote "` + name + `"`
	if file.Has(group, "gpg-verify") && !file.Bool(group, "gpg-verify") {
		return nil, fmt.Errorf("flatpak: remote %s is not signed, and cpak does not read unsigned repositories", name)
	}
	key, err := os.ReadFile(filepath.Join(i.Path, "repo", name+".trustedkeys.gpg"))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("flatpak: the %s installation holds no key for remote %s", i.Kind, name)
	}
	return key, err
}

func (i Installation) repoConfig() (*KeyFile, error) {
	data, err := os.ReadFile(filepath.Join(i.Path, "repo", "config"))
	if err != nil {
		return nil, fmt.Errorf("flatpak: read the %s installation remotes: %w", i.Kind, err)
	}
	return ParseKeyFile(data)
}
