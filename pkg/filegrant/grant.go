/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package filegrant

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const GuestRoot = "/run/cpak/grants"

const (
	KindFile      = "file"
	KindDirectory = "directory"

	AccessReadOnly  = "read-only"
	AccessReadWrite = "read-write"

	LifetimeSession    = "session"
	LifetimePersistent = "persistent"
)

type Grant struct {
	ID          string `json:"id"`
	Origin      string `json:"origin"`
	Selection   string `json:"selection"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	MountTarget string `json:"mount_target"`
	Kind        string `json:"kind"`
	Access      string `json:"access"`
	Lifetime    string `json:"lifetime"`
}

func Resolve(origin, selected, access, lifetime string, containingFolder bool) (Grant, error) {
	if strings.TrimSpace(origin) == "" {
		return Grant{}, errors.New("file grant origin is required")
	}
	if access != AccessReadOnly && access != AccessReadWrite {
		return Grant{}, fmt.Errorf("unsupported file grant access: %s", access)
	}
	if lifetime != LifetimeSession && lifetime != LifetimePersistent {
		return Grant{}, fmt.Errorf("unsupported file grant lifetime: %s", lifetime)
	}
	abs, err := filepath.Abs(selected)
	if err != nil {
		return Grant{}, fmt.Errorf("resolve selected path: %w", err)
	}
	selection, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return Grant{}, fmt.Errorf("resolve selected path: %w", err)
	}
	info, err := os.Stat(selection)
	if err != nil {
		return Grant{}, fmt.Errorf("inspect selected path: %w", err)
	}
	kind := KindFile
	source := selection
	if info.IsDir() {
		kind = KindDirectory
	} else if !info.Mode().IsRegular() {
		return Grant{}, errors.New("selected path must be a regular file or directory")
	}
	if containingFolder && kind == KindFile {
		source = filepath.Dir(selection)
		kind = KindDirectory
	}
	return build(origin, selection, source, kind, access, lifetime)
}

func ResolveSave(origin, selected, lifetime string) (Grant, error) {
	if strings.TrimSpace(origin) == "" {
		return Grant{}, errors.New("file grant origin is required")
	}
	if lifetime != LifetimeSession && lifetime != LifetimePersistent {
		return Grant{}, fmt.Errorf("unsupported file grant lifetime: %s", lifetime)
	}
	abs, err := filepath.Abs(selected)
	if err != nil {
		return Grant{}, fmt.Errorf("resolve selected path: %w", err)
	}
	abs = filepath.Clean(abs)
	if filepath.Base(abs) == "." || filepath.Base(abs) == string(filepath.Separator) {
		return Grant{}, errors.New("save file name is invalid")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return Grant{}, fmt.Errorf("resolve save directory: %w", err)
	}
	selection := filepath.Join(parent, filepath.Base(abs))
	return build(origin, selection, parent, KindDirectory, AccessReadWrite, lifetime)
}

func build(origin, selection, source, kind, access, lifetime string) (Grant, error) {
	id := grantID(origin, selection, source, kind, access)
	name := filepath.Base(source)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "selection"
	}
	mountTarget := filepath.Join(GuestRoot, id, name)
	target := mountTarget
	if selection != source {
		target = filepath.Join(mountTarget, filepath.Base(selection))
	}
	return Grant{
		ID:          id,
		Origin:      origin,
		Selection:   selection,
		Source:      source,
		Target:      target,
		MountTarget: mountTarget,
		Kind:        kind,
		Access:      access,
		Lifetime:    lifetime,
	}, nil
}

func grantID(origin, selection, source, kind, access string) string {
	digest := sha256.Sum256([]byte(origin + "\x00" + selection + "\x00" + source + "\x00" + kind + "\x00" + access))
	return hex.EncodeToString(digest[:])
}

func (g Grant) Validate() error {
	if len(g.ID) != sha256.Size*2 {
		return errors.New("file grant ID is invalid")
	}
	if _, err := hex.DecodeString(g.ID); err != nil {
		return errors.New("file grant ID is invalid")
	}
	if g.Origin == "" || !filepath.IsAbs(g.Selection) || filepath.Clean(g.Selection) != g.Selection || !filepath.IsAbs(g.Source) || filepath.Clean(g.Source) != g.Source {
		return errors.New("file grant source is invalid")
	}
	root := filepath.Join(GuestRoot, g.ID)
	expectedMount := filepath.Join(root, filepath.Base(g.Source))
	expectedTarget := expectedMount
	if g.Selection != g.Source {
		expectedTarget = filepath.Join(expectedMount, filepath.Base(g.Selection))
	}
	if g.MountTarget != expectedMount || g.Target != expectedTarget || !pathContains(root, g.Target) {
		return errors.New("file grant target is invalid")
	}
	if g.Kind != KindFile && g.Kind != KindDirectory {
		return errors.New("file grant kind is invalid")
	}
	if g.Access != AccessReadOnly && g.Access != AccessReadWrite {
		return errors.New("file grant access is invalid")
	}
	if g.ID != grantID(g.Origin, g.Selection, g.Source, g.Kind, g.Access) {
		return errors.New("file grant ID does not match its scope")
	}
	if g.Lifetime != LifetimeSession && g.Lifetime != LifetimePersistent {
		return errors.New("file grant lifetime is invalid")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
