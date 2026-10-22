/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/ostree"
)

// The Flathub key is pinned in the binary, so the first install from the
// default remote does not have to trust whatever answers at dl.flathub.org.
//
//go:embed flathub.gpg
var flathubKey []byte

const (
	DefaultRemote = "flathub"
	flathubURL    = "https://dl.flathub.org/repo/"
)

// Remote is a Flatpak repository cpak installs from and the key it trusts it
// through.
type Remote struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
	Key   []byte `json:"key"`
}

// Repository answers with a client for the remote.
func (r Remote) Repository(userAgent string) (*ostree.Repository, error) {
	keyring, err := ostree.ParseKeyring(r.Key)
	if err != nil {
		return nil, fmt.Errorf("flatpak: remote %s: %w", r.Name, err)
	}
	return &ostree.Repository{URL: r.URL, Keyring: keyring, UserAgent: userAgent}, nil
}

// Fingerprints names the keys a remote is trusted through.
func (r Remote) Fingerprints() ([]string, error) {
	keyring, err := ostree.ParseKeyring(r.Key)
	if err != nil {
		return nil, err
	}
	return keyring.Fingerprints(), nil
}

// ValidateRemoteName keeps a remote name usable as one element of an origin.
func ValidateRemoteName(name string) error {
	if name == "" || len(name) > 64 {
		return fmt.Errorf("flatpak: invalid remote name %q", name)
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_':
		default:
			return fmt.Errorf("flatpak: remote name %q may only hold lowercase letters, digits, - and _", name)
		}
	}
	return nil
}

func validRemoteURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("flatpak: invalid repository URL %q", value)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("flatpak: repository %s is not served over HTTPS", value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("flatpak: repository URL %q carries more than a location", value)
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}
	return parsed.String(), nil
}

// loopbackHost lets a repository served on this machine be read over plain
// HTTP, which carries nothing over a network; the signatures are what make
// it trusted either way.
func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Remotes is the set of remotes a user has configured, with Flathub always
// present.
type Remotes struct {
	path    string
	entries map[string]Remote
}

func builtinRemotes() map[string]Remote {
	return map[string]Remote{
		DefaultRemote: {Name: DefaultRemote, Title: "Flathub", URL: flathubURL, Key: append([]byte{}, flathubKey...)},
	}
}

// LoadRemotes reads the remotes file. A missing file is a configuration with
// only the built-in remotes, which cannot be removed or replaced: a file that
// tries is refused, since a changed key for Flathub is the one edit nobody
// should make without noticing.
func LoadRemotes(file string) (*Remotes, error) {
	remotes := &Remotes{path: file, entries: builtinRemotes()}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return remotes, nil
	}
	if err != nil {
		return nil, fmt.Errorf("flatpak: read remotes: %w", err)
	}
	var stored []Remote
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("flatpak: decode remotes: %w", err)
	}
	for _, remote := range stored {
		if err := remotes.add(remote); err != nil {
			return nil, err
		}
	}
	return remotes, nil
}

func (r *Remotes) add(remote Remote) error {
	if err := ValidateRemoteName(remote.Name); err != nil {
		return err
	}
	if _, builtin := builtinRemotes()[remote.Name]; builtin {
		return fmt.Errorf("flatpak: remote %s is built in and cannot be redefined", remote.Name)
	}
	location, err := validRemoteURL(remote.URL)
	if err != nil {
		return err
	}
	remote.URL = location
	if _, err := remote.Fingerprints(); err != nil {
		return fmt.Errorf("flatpak: remote %s: %w", remote.Name, err)
	}
	r.entries[remote.Name] = remote
	return nil
}

// Get answers with a configured remote.
func (r *Remotes) Get(name string) (Remote, error) {
	remote, ok := r.entries[name]
	if !ok {
		return Remote{}, fmt.Errorf("flatpak: no remote named %s; add it from its .flatpakrepo first", name)
	}
	return remote, nil
}

// ByURL finds the remote serving a repository URL.
func (r *Remotes) ByURL(location string) (Remote, bool) {
	normalized, err := validRemoteURL(location)
	if err != nil {
		return Remote{}, false
	}
	for _, remote := range r.entries {
		if remote.URL == normalized {
			return remote, true
		}
	}
	return Remote{}, false
}

// List answers with the remotes by name.
func (r *Remotes) List() []Remote {
	values := make([]Remote, 0, len(r.entries))
	for _, remote := range r.entries {
		values = append(values, remote)
	}
	sort.Slice(values, func(first, second int) bool { return values[first].Name < values[second].Name })
	return values
}

// Add records a remote and writes the file.
func (r *Remotes) Add(remote Remote) error {
	if existing, ok := r.entries[remote.Name]; ok {
		if existing.URL == remote.URL && string(existing.Key) == string(remote.Key) {
			return nil
		}
		return fmt.Errorf("flatpak: a different remote named %s is already configured", remote.Name)
	}
	if err := r.add(remote); err != nil {
		return err
	}
	return r.save()
}

func (r *Remotes) save() error {
	stored := make([]Remote, 0, len(r.entries))
	for _, remote := range r.List() {
		if _, builtin := builtinRemotes()[remote.Name]; !builtin {
			stored = append(stored, remote)
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("flatpak: write remotes: %w", err)
	}
	temporary := r.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("flatpak: write remotes: %w", err)
	}
	return os.Rename(temporary, r.path)
}

// RepoFile is a .flatpakrepo description of a remote.
type RepoFile struct {
	Title string
	URL   string
	Key   []byte
}

// ParseRepoFile reads a .flatpakrepo file. A description without a key is
// refused: cpak never reads a repository it has no key for.
func ParseRepoFile(data []byte) (RepoFile, error) {
	file, err := ParseKeyFile(data)
	if err != nil {
		return RepoFile{}, err
	}
	const group = "Flatpak Repo"
	location, err := validRemoteURL(file.String(group, "Url"))
	if err != nil {
		return RepoFile{}, err
	}
	key, err := decodeKey(file.String(group, "GPGKey"))
	if err != nil {
		return RepoFile{}, err
	}
	return RepoFile{Title: file.String(group, "Title"), URL: location, Key: key}, nil
}

// RefFile is a .flatpakref description of an application.
type RefFile struct {
	Name              string
	Branch            string
	Title             string
	URL               string
	Key               []byte
	Runtime           bool
	SuggestRemoteName string
}

// ParseRefFile reads a .flatpakref file.
func ParseRefFile(data []byte) (RefFile, error) {
	file, err := ParseKeyFile(data)
	if err != nil {
		return RefFile{}, err
	}
	const group = "Flatpak Ref"
	ref := RefFile{
		Name:              file.String(group, "Name"),
		Branch:            file.String(group, "Branch"),
		Title:             file.String(group, "Title"),
		Runtime:           file.Bool(group, "IsRuntime"),
		SuggestRemoteName: strings.ToLower(file.String(group, "SuggestRemoteName")),
	}
	if err := ValidateID(ref.Name); err != nil {
		return RefFile{}, err
	}
	if ref.Branch == "" {
		ref.Branch = "master"
	}
	if !validPart(ref.Branch) {
		return RefFile{}, fmt.Errorf("flatpak: invalid branch %q", ref.Branch)
	}
	if ref.URL, err = validRemoteURL(file.String(group, "Url")); err != nil {
		return RefFile{}, err
	}
	if ref.Key, err = decodeKey(file.String(group, "GPGKey")); err != nil {
		return RefFile{}, err
	}
	return ref, nil
}

func decodeKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errors.New("flatpak: the description carries no GPGKey, and cpak does not read unsigned repositories")
	}
	key, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return nil, fmt.Errorf("flatpak: decode GPGKey: %w", err)
	}
	if _, err := ostree.ParseKeyring(key); err != nil {
		return nil, fmt.Errorf("flatpak: GPGKey: %w", err)
	}
	return key, nil
}
