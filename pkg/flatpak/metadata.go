/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Metadata is the metadata keyfile of an application or runtime commit.
type Metadata struct {
	Runtime bool

	// Name is the id the metadata describes.
	Name string

	// RuntimeRef is the runtime an application runs on; empty for runtimes.
	RuntimeRef Ref

	// Command is the binary an application starts, relative to /app/bin
	// unless it is absolute.
	Command string

	Context     Context
	SessionBus  map[string]string
	SystemBus   map[string]string
	Environment map[string]string
	Extensions  []ExtensionPoint

	// ExtensionOf is set on an extension, naming what it extends.
	ExtensionOf string

	// ExtraData are the files an application downloads from its publisher at
	// install time and turns into /app/extra with its apply_extra script.
	ExtraData []ExtraData
}

// ExtraData is one file of the [Extra Data] group.
type ExtraData struct {
	Name     string
	URI      string
	Checksum string
	Size     uint64
}

// Context is the [Context] group, each list as the publisher wrote it.
type Context struct {
	Shared      []string
	Sockets     []string
	Devices     []string
	Features    []string
	Filesystems []string
	Persistent  []string
}

// ExtensionPoint is an [Extension ...] group: a directory of the application
// or runtime where matching extensions are mounted.
type ExtensionPoint struct {
	Name           string
	Directory      string
	Versions       []string
	AddLdPath      string
	Subdirectories bool
	NoAutoDownload bool
	LocaleSubset   bool
	Autodelete     bool

	// MergeDirs are directories each installed subdirectory extension
	// contributes to, which Flatpak presents merged under the point.
	MergeDirs []string
}

// ParseMetadata reads a metadata keyfile.
func ParseMetadata(data []byte) (Metadata, error) {
	file, err := ParseKeyFile(data)
	if err != nil {
		return Metadata{}, err
	}
	var meta Metadata
	switch {
	case file.Has("Application", "name"):
		meta.Name = file.String("Application", "name")
		runtime, err := ParseRuntime(file.String("Application", "runtime"))
		if err != nil {
			return Metadata{}, fmt.Errorf("flatpak: %s names an invalid runtime: %w", meta.Name, err)
		}
		meta.RuntimeRef = runtime
		meta.Command = file.String("Application", "command")
	case file.Has("Runtime", "name"):
		meta.Runtime = true
		meta.Name = file.String("Runtime", "name")
	default:
		return Metadata{}, errors.New("flatpak: metadata describes neither an application nor a runtime")
	}
	if err := ValidateID(meta.Name); err != nil {
		return Metadata{}, err
	}
	if meta.Command != "" {
		if err := validCommand(meta.Command); err != nil {
			return Metadata{}, err
		}
	}
	meta.Context = Context{
		Shared:      file.List("Context", "shared"),
		Sockets:     file.List("Context", "sockets"),
		Devices:     file.List("Context", "devices"),
		Features:    file.List("Context", "features"),
		Filesystems: file.List("Context", "filesystems"),
		Persistent:  file.List("Context", "persistent"),
	}
	meta.SessionBus = file.Keys("Session Bus Policy")
	meta.SystemBus = file.Keys("System Bus Policy")
	meta.Environment = file.Keys("Environment")
	meta.ExtensionOf = file.String("ExtensionOf", "ref")
	for _, group := range file.Groups() {
		name, ok := strings.CutPrefix(group, "Extension ")
		if !ok {
			continue
		}
		point, err := parseExtensionPoint(file, group, name)
		if err != nil {
			return Metadata{}, err
		}
		meta.Extensions = append(meta.Extensions, point)
	}
	sort.Slice(meta.Extensions, func(first, second int) bool {
		return meta.Extensions[first].Name < meta.Extensions[second].Name
	})
	extra, err := parseExtraData(file)
	if err != nil {
		return Metadata{}, fmt.Errorf("flatpak: %s: %w", meta.Name, err)
	}
	meta.ExtraData = extra
	return meta, nil
}

var extraChecksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// parseExtraData reads the [Extra Data] group. The first file uses the plain
// keys and every further one the same keys with a number appended.
func parseExtraData(file *KeyFile) ([]ExtraData, error) {
	const group = "Extra Data"
	files := []ExtraData{}
	for index := 0; ; index++ {
		suffix := ""
		if index > 0 {
			suffix = strconv.Itoa(index)
		}
		if !file.Has(group, "uri"+suffix) {
			return files, nil
		}
		data := ExtraData{
			Name:     file.String(group, "name"+suffix),
			URI:      file.String(group, "uri"+suffix),
			Checksum: strings.ToLower(file.String(group, "checksum"+suffix)),
		}
		size, err := strconv.ParseUint(file.String(group, "size"+suffix), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("extra data %q has no valid size", data.Name)
		}
		data.Size = size
		if data.Name == "" || data.Name == "." || data.Name == ".." || strings.ContainsAny(data.Name, "/\x00") {
			return nil, fmt.Errorf("extra data %q is not a plain file name", data.Name)
		}
		if !extraChecksumPattern.MatchString(data.Checksum) {
			return nil, fmt.Errorf("extra data %q has no sha256 checksum", data.Name)
		}
		location, err := url.Parse(data.URI)
		if err != nil || (location.Scheme != "https" && location.Scheme != "http") || location.Host == "" {
			return nil, fmt.Errorf("extra data %q has an unsupported address", data.Name)
		}
		files = append(files, data)
	}
}

func validCommand(command string) error {
	clean := path.Clean(command)
	if clean != command || strings.ContainsAny(command, "\x00\n") || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("flatpak: invalid command %q", command)
	}
	return nil
}

func parseExtensionPoint(file *KeyFile, group, name string) (ExtensionPoint, error) {
	if err := ValidateID(name); err != nil {
		return ExtensionPoint{}, fmt.Errorf("flatpak: extension point: %w", err)
	}
	directory := file.String(group, "directory")
	clean := path.Clean(directory)
	if directory == "" || strings.HasPrefix(directory, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ExtensionPoint{}, fmt.Errorf("flatpak: extension point %s has an invalid directory %q", name, directory)
	}
	point := ExtensionPoint{
		Name:           name,
		Directory:      clean,
		AddLdPath:      file.String(group, "add-ld-path"),
		Subdirectories: file.Bool(group, "subdirectories"),
		NoAutoDownload: file.Bool(group, "no-autodownload"),
		LocaleSubset:   file.Bool(group, "locale-subset"),
		Autodelete:     file.Bool(group, "autodelete"),
	}
	for _, dir := range file.List(group, "merge-dirs") {
		clean := path.Clean(dir)
		if strings.HasPrefix(clean, "/") || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return ExtensionPoint{}, fmt.Errorf("flatpak: extension point %s has an invalid merge directory %q", name, dir)
		}
		point.MergeDirs = append(point.MergeDirs, clean)
	}
	switch {
	case file.Has(group, "versions"):
		point.Versions = file.List(group, "versions")
	case file.Has(group, "version"):
		point.Versions = []string{file.String(group, "version")}
	}
	if point.AddLdPath != "" {
		clean := path.Clean(point.AddLdPath)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return ExtensionPoint{}, fmt.Errorf("flatpak: extension point %s has an invalid add-ld-path", name)
		}
		point.AddLdPath = clean
	}
	return point, nil
}

// Matches reports whether an extension id belongs to this point: the id
// itself, or with subdirectories one more element below it.
func (p ExtensionPoint) Matches(id string) bool {
	if id == p.Name {
		return true
	}
	if !p.Subdirectories {
		return false
	}
	rest, ok := strings.CutPrefix(id, p.Name+".")
	return ok && rest != "" && !strings.Contains(rest, ".")
}

// MountPoint is where an extension is placed below the point's directory.
func (p ExtensionPoint) MountPoint(id string) string {
	if id == p.Name || !p.Subdirectories {
		return p.Directory
	}
	return path.Join(p.Directory, strings.TrimPrefix(id, p.Name+"."))
}

// Branches answers with the branches an extension is looked up on, given the
// branch of what it extends.
func (p ExtensionPoint) Branches(fallback string) []string {
	if len(p.Versions) > 0 {
		return p.Versions
	}
	return []string{fallback}
}
