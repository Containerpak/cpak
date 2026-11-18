/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/ostree"
)

const maxAppstream = 128 << 20

type FlatpakRemoteInfo struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	Builtin      bool     `json:"builtin"`
	Removable    bool     `json:"removable"`
	Fingerprints []string `json:"fingerprints"`
}

type FlatpakCatalogApp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Summary   string `json:"summary,omitempty"`
	Developer string `json:"developer,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Remote    string `json:"remote"`
	Branch    string `json:"branch"`
	Origin    string `json:"origin"`
}

func describeRemote(remote flatpak.Remote) FlatpakRemoteInfo {
	fingerprints, _ := remote.Fingerprints()
	title := remote.Title
	if title == "" {
		title = remote.Name
	}
	builtin := flatpak.IsBuiltinRemote(remote.Name)
	return FlatpakRemoteInfo{
		Name:         remote.Name,
		Title:        title,
		URL:          remote.URL,
		Builtin:      builtin,
		Removable:    !builtin,
		Fingerprints: fingerprints,
	}
}

func (c *Cpak) FlatpakRemoteList() ([]FlatpakRemoteInfo, error) {
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return nil, err
	}
	listed := []FlatpakRemoteInfo{}
	for _, remote := range remotes.List() {
		listed = append(listed, describeRemote(remote))
	}
	return listed, nil
}

func (c *Cpak) AddFlatpakRemote(name string, description []byte) (FlatpakRemoteInfo, error) {
	if err := flatpak.ValidateRemoteName(name); err != nil {
		return FlatpakRemoteInfo{}, err
	}
	file, err := flatpak.ParseRepoFile(description)
	if err != nil {
		return FlatpakRemoteInfo{}, err
	}
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return FlatpakRemoteInfo{}, err
	}
	if existing, known := remotes.ByURL(file.URL); known && existing.Name != name {
		return FlatpakRemoteInfo{}, fmt.Errorf("the repository %s is already configured as %s", file.URL, existing.Name)
	}
	remote := flatpak.Remote{Name: name, Title: file.Title, URL: file.URL, Key: file.Key}
	repository, err := remote.Repository(flatpakUserAgent)
	if err != nil {
		return FlatpakRemoteInfo{}, err
	}
	if _, err := repository.FetchSummary(c.Ctx, flatpak.HostArch()); err != nil {
		return FlatpakRemoteInfo{}, fmt.Errorf("the repository at %s could not be verified: %w", file.URL, err)
	}
	if err := remotes.Add(remote); err != nil {
		return FlatpakRemoteInfo{}, err
	}
	return describeRemote(remote), nil
}

func (c *Cpak) RemoveFlatpakRemote(name string) error {
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return err
	}
	if err := remotes.Remove(name); err != nil {
		return err
	}
	return os.RemoveAll(c.appstreamCachePath(name))
}

func (c *Cpak) appstreamCachePath(remote string) string {
	return filepath.Join(c.Options.CachePath, "flatpak-appstream", remote)
}

func (c *Cpak) FlatpakRemoteApps(name, arch string) ([]FlatpakCatalogApp, error) {
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return nil, err
	}
	remote, err := remotes.Get(name)
	if err != nil {
		return nil, err
	}
	if arch == "" {
		arch = flatpak.HostArch()
	}
	repository, err := remote.Repository(flatpakUserAgent)
	if err != nil {
		return nil, err
	}
	summary, err := repository.FetchSummary(c.Ctx, arch)
	if err != nil {
		return nil, err
	}
	branches := map[string]string{}
	for ref := range summary.Refs {
		rest, ok := strings.CutPrefix(ref, "app/")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[1] != arch || flatpak.ValidateID(parts[0]) != nil {
			continue
		}
		current, seen := branches[parts[0]]
		if !seen || parts[2] == "stable" || (current != "stable" && parts[2] < current) {
			branches[parts[0]] = parts[2]
		}
	}
	entry := func(id string) FlatpakCatalogApp {
		return FlatpakCatalogApp{ID: id, Name: id, Remote: name, Branch: branches[id], Origin: FlatpakOrigin(name, id)}
	}
	described := map[string]FlatpakCatalogApp{}
	for _, component := range c.flatpakAppstream(repository, summary, name, arch) {
		id := strings.TrimSuffix(component.ID, ".desktop")
		if _, published := branches[id]; !published {
			continue
		}
		app := entry(id)
		if text := component.name(); text != "" {
			app.Name = text
		}
		app.Summary = component.summary()
		app.Developer = component.developer()
		app.Icon = component.Icon
		described[id] = app
	}
	apps := make([]FlatpakCatalogApp, 0, len(branches))
	for id := range branches {
		app, ok := described[id]
		if !ok {
			app = entry(id)
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(first, second int) bool {
		return strings.ToLower(apps[first].Name) < strings.ToLower(apps[second].Name)
	})
	return apps, nil
}

type appstreamText struct {
	Lang  string `xml:"lang,attr"`
	Value string `xml:",chardata"`
}

type appstreamIcon struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type appstreamComponent struct {
	Type          string          `xml:"type,attr"`
	ID            string          `xml:"id"`
	Names         []appstreamText `xml:"name"`
	Summaries     []appstreamText `xml:"summary"`
	DeveloperName []appstreamText `xml:"developer_name"`
	Developer     []appstreamText `xml:"developer>name"`
	Icons         []appstreamIcon `xml:"icon"`
	Icon          string          `xml:"-"`
}

type appstreamDocument struct {
	Components []appstreamComponent `xml:"component"`
}

func untranslated(values []appstreamText) string {
	for _, value := range values {
		if value.Lang == "" {
			return strings.Join(strings.Fields(value.Value), " ")
		}
	}
	return ""
}

func (a appstreamComponent) name() string    { return untranslated(a.Names) }
func (a appstreamComponent) summary() string { return untranslated(a.Summaries) }

func (a appstreamComponent) developer() string {
	if name := untranslated(a.Developer); name != "" {
		return name
	}
	return untranslated(a.DeveloperName)
}

func parseAppstream(data []byte) ([]appstreamComponent, error) {
	var document appstreamDocument
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode appstream: %w", err)
	}
	components := []appstreamComponent{}
	for _, component := range document.Components {
		if component.Type != "" && component.Type != "desktop" && component.Type != "desktop-application" {
			continue
		}
		components = append(components, component)
	}
	return components, nil
}

func (c *Cpak) flatpakAppstream(repository *ostree.Repository, summary ostree.Summary, remote, arch string) []appstreamComponent {
	for _, ref := range []string{"appstream2/" + arch, "appstream/" + arch} {
		listed, ok := summary.Refs[ref]
		if !ok {
			continue
		}
		components, err := c.readAppstream(repository, ref, listed.Checksum, remote)
		if err == nil {
			return components
		}
	}
	return nil
}

func (c *Cpak) readAppstream(repository *ostree.Repository, ref string, checksum ostree.Checksum, remote string) ([]appstreamComponent, error) {
	commit, err := repository.FetchCommit(c.Ctx, ref, checksum)
	if err != nil {
		return nil, err
	}
	entries, err := repository.Tree(c.Ctx, commit)
	if err != nil {
		return nil, err
	}
	files := map[string]ostree.Checksum{}
	for _, entry := range entries {
		if entry.Kind == ostree.EntryFile {
			files[strings.TrimPrefix(entry.Path, "/")] = entry.Checksum
		}
	}
	var data []byte
	if object, ok := files["appstream.xml"]; ok {
		data, err = c.readFlatpakFile(repository, object)
	} else if object, ok := files["appstream.xml.gz"]; ok {
		var compressed []byte
		if compressed, err = c.readFlatpakFile(repository, object); err == nil {
			data, err = gunzipLimited(compressed)
		}
	} else {
		err = errors.New("the appstream commit carries no appstream.xml")
	}
	if err != nil {
		return nil, err
	}
	components, err := parseAppstream(data)
	if err != nil {
		return nil, err
	}
	iconDir := filepath.Join(c.appstreamCachePath(remote), "icons")
	for index := range components {
		components[index].Icon = c.appstreamIcon(repository, files, iconDir, components[index])
	}
	return components, nil
}

func (c *Cpak) appstreamIcon(repository *ostree.Repository, files map[string]ostree.Checksum, iconDir string, component appstreamComponent) string {
	for _, size := range []string{"128x128", "64x64"} {
		for _, icon := range component.Icons {
			name := filepath.Base(strings.TrimSpace(icon.Value))
			if icon.Type != "cached" || name == "." || name == "/" {
				continue
			}
			object, ok := files["icons/"+size+"/"+name]
			if !ok {
				continue
			}
			target := filepath.Join(iconDir, object.String()+filepath.Ext(name))
			if _, err := os.Stat(target); err == nil {
				return target
			}
			data, err := c.readFlatpakFile(repository, object)
			if err != nil || os.MkdirAll(iconDir, 0o700) != nil {
				continue
			}
			temporary := target + ".tmp"
			if os.WriteFile(temporary, data, 0o600) != nil || os.Rename(temporary, target) != nil {
				os.Remove(temporary)
				continue
			}
			return target
		}
	}
	for _, icon := range component.Icons {
		location := strings.TrimSpace(icon.Value)
		if icon.Type == "remote" && strings.HasPrefix(location, "https://") {
			return location
		}
	}
	return ""
}

func (c *Cpak) readFlatpakFile(repository *ostree.Repository, checksum ostree.Checksum) ([]byte, error) {
	var object bytes.Buffer
	size, err := repository.FetchFileObject(c.Ctx, checksum, &object)
	if err != nil {
		return nil, err
	}
	header, content, err := ostree.OpenFileObject(checksum, bytes.NewReader(object.Bytes()), size)
	if err != nil {
		return nil, err
	}
	if !header.IsRegular() {
		return nil, fmt.Errorf("object %s is not a regular file", checksum)
	}
	data, err := io.ReadAll(io.LimitReader(content, maxAppstream+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAppstream {
		return nil, fmt.Errorf("object %s is too large", checksum)
	}
	return data, nil
}

func gunzipLimited(compressed []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxAppstream+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAppstream {
		return nil, errors.New("the appstream data is too large")
	}
	return data, nil
}
