/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package filegrant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

const storeVersion = 1

type Store struct {
	Directory string
}

type document struct {
	Version int     `json:"version"`
	Grants  []Grant `json:"grants"`
}

func (s Store) Load(origin string) ([]Grant, error) {
	file, err := s.lock(origin)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readDocument(file, origin)
}

func (s Store) Add(grant Grant) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if grant.Lifetime != LifetimePersistent {
		return errors.New("only persistent file grants can be stored")
	}
	file, err := s.lock(grant.Origin)
	if err != nil {
		return err
	}
	defer file.Close()
	grants, err := readDocument(file, grant.Origin)
	if err != nil {
		return err
	}
	replaced := false
	for index := range grants {
		if grants[index].ID == grant.ID {
			grants[index] = grant
			replaced = true
			break
		}
	}
	if !replaced {
		grants = append(grants, grant)
	}
	return writeDocument(file, grants)
}

func (s Store) Remove(origin, id string) error {
	file, err := s.lock(origin)
	if err != nil {
		return err
	}
	defer file.Close()
	grants, err := readDocument(file, origin)
	if err != nil {
		return err
	}
	kept := grants[:0]
	for _, grant := range grants {
		if grant.ID != id {
			kept = append(kept, grant)
		}
	}
	if len(kept) == len(grants) {
		return os.ErrNotExist
	}
	return writeDocument(file, kept)
}

func (s Store) Clear(origin string) error {
	path, err := s.path(origin)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file grant store: %w", err)
	}
	return nil
}

func (s Store) lock(origin string) (*os.File, error) {
	path, err := s.path(origin)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return nil, fmt.Errorf("create file grant store: %w", err)
	}
	if err := os.Chmod(s.Directory, 0700); err != nil {
		return nil, fmt.Errorf("restrict file grant store: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open file grant store: %w", err)
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("restrict file grant store: %w", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock file grant store: %w", err)
	}
	return file, nil
}

func (s Store) path(origin string) (string, error) {
	if origin == "" {
		return "", errors.New("file grant origin is required")
	}
	if !filepath.IsAbs(s.Directory) {
		return "", errors.New("file grant store must be absolute")
	}
	digest := sha256.Sum256([]byte(origin))
	return filepath.Join(s.Directory, hex.EncodeToString(digest[:])+".json"), nil
}

func readDocument(file *os.File, origin string) ([]Grant, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return []Grant{}, nil
	}
	var value document
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode file grant store: %w", err)
	}
	if value.Version != storeVersion {
		return nil, fmt.Errorf("unsupported file grant store version: %d", value.Version)
	}
	for _, grant := range value.Grants {
		if grant.Origin != origin {
			return nil, errors.New("file grant store origin does not match")
		}
		if err = grant.Validate(); err != nil {
			return nil, err
		}
	}
	sort.Slice(value.Grants, func(i, j int) bool { return value.Grants[i].Target < value.Grants[j].Target })
	return value.Grants, nil
}

func writeDocument(file *os.File, grants []Grant) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Target < grants[j].Target })
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document{Version: storeVersion, Grants: grants}); err != nil {
		return fmt.Errorf("encode file grant store: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync file grant store: %w", err)
	}
	return nil
}
