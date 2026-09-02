/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "never"
	RestartOnFailure RestartPolicy = "on-failure"
	RestartAlways    RestartPolicy = "always"
)

type Definition struct {
	Name             string        `json:"name"`
	Origin           string        `json:"origin"`
	Branch           string        `json:"branch,omitempty"`
	Commit           string        `json:"commit,omitempty"`
	Release          string        `json:"release,omitempty"`
	ManifestService  string        `json:"manifest_service,omitempty"`
	Binary           string        `json:"binary,omitempty"`
	Arguments        []string      `json:"arguments,omitempty"`
	Environment      []string      `json:"environment,omitempty"`
	EnvironmentFiles []string      `json:"environment_files,omitempty"`
	Secrets          []string      `json:"secrets,omitempty"`
	DependsOn        []string      `json:"depends_on,omitempty"`
	Restart          RestartPolicy `json:"restart"`
	HealthCommand    string        `json:"health_command,omitempty"`
	HealthDelay      int           `json:"health_delay,omitempty"`
	HealthInterval   int           `json:"health_interval,omitempty"`
	HealthRetries    int           `json:"health_retries,omitempty"`
	HealthTimeout    int           `json:"health_timeout,omitempty"`
	Enabled          bool          `json:"enabled"`
}

func (d Definition) Instance() string {
	return "service-" + d.Name
}

func (d Definition) Validate() error {
	if !validName(d.Name) {
		return fmt.Errorf("invalid service name %q", d.Name)
	}
	if strings.TrimSpace(d.Origin) == "" {
		return errors.New("service origin is required")
	}
	if strings.ContainsAny(d.Origin, "\x00\r\n") {
		return errors.New("service origin contains a control character")
	}
	selectors := 0
	for _, value := range []string{d.Branch, d.Commit, d.Release} {
		if value != "" {
			selectors++
		}
	}
	if selectors > 1 {
		return errors.New("branch, commit and release are mutually exclusive")
	}
	if d.ManifestService != "" && d.Binary != "" {
		return errors.New("manifest service and binary are mutually exclusive")
	}
	if d.Binary == "" && len(d.Arguments) > 0 {
		return errors.New("service arguments require a binary")
	}
	if d.ManifestService != "" && !validName(d.ManifestService) {
		return fmt.Errorf("invalid manifest service name %q", d.ManifestService)
	}
	for _, entry := range d.Environment {
		if err := validateEnvironmentEntry(entry); err != nil {
			return err
		}
	}
	for _, path := range d.EnvironmentFiles {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("environment file path must be absolute: %q", path)
		}
	}
	for _, secret := range d.Secrets {
		if err := validateSecretEntry(secret); err != nil {
			return err
		}
	}
	switch d.Restart {
	case RestartNever, RestartOnFailure, RestartAlways:
	default:
		return fmt.Errorf("unsupported restart policy %q", d.Restart)
	}
	if d.HealthDelay < 0 || d.HealthInterval < 0 || d.HealthRetries < 0 || d.HealthTimeout < 0 {
		return errors.New("health values cannot be negative")
	}
	seen := make(map[string]bool, len(d.DependsOn))
	for _, dependency := range d.DependsOn {
		if !validName(dependency) {
			return fmt.Errorf("invalid dependency name %q", dependency)
		}
		if dependency == d.Name {
			return fmt.Errorf("service %s depends on itself", d.Name)
		}
		if seen[dependency] {
			return fmt.Errorf("service %s repeats dependency %s", d.Name, dependency)
		}
		seen[dependency] = true
	}
	return nil
}

func validateEnvironmentEntry(entry string) error {
	name, _, found := strings.Cut(entry, "=")
	if !found || !validEnvironmentName(name) {
		return fmt.Errorf("invalid environment entry %q", entry)
	}
	if strings.ContainsAny(entry, "\x00\r\n") {
		return errors.New("environment entries cannot contain control characters")
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func validateSecretEntry(entry string) error {
	name, source, found := strings.Cut(entry, "=")
	if !found || !validEnvironmentName(name) || !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return fmt.Errorf("invalid secret entry %q", entry)
	}
	return nil
}

func validName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

type Store struct {
	Directory string
}

func (s Store) Put(definition Definition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if err := s.prepare(); err != nil {
		return err
	}
	path := s.definitionPath(definition.Name)
	temporary, err := os.CreateTemp(s.Directory, ".service-*")
	if err != nil {
		return fmt.Errorf("create service definition: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		encoder := json.NewEncoder(temporary)
		encoder.SetEscapeHTML(false)
		err = encoder.Encode(definition)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write service definition: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace service definition: %w", err)
	}
	return syncDirectory(s.Directory)
}

func (s Store) Get(name string) (Definition, error) {
	if !validName(name) {
		return Definition{}, fmt.Errorf("invalid service name %q", name)
	}
	definition, err := readDefinition(s.definitionPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return Definition{}, fmt.Errorf("service %s is not registered", name)
	}
	return definition, err
}

func (s Store) Delete(name string) error {
	if !validName(name) {
		return fmt.Errorf("invalid service name %q", name)
	}
	if err := os.Remove(s.definitionPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove service definition: %w", err)
	}
	return syncDirectory(s.Directory)
}

func (s Store) Remove(name string) error {
	definitions, err := s.List()
	if err != nil {
		return err
	}
	found := false
	for _, definition := range definitions {
		if definition.Name == name {
			found = true
			continue
		}
		for _, dependency := range definition.DependsOn {
			if dependency == name {
				return fmt.Errorf("service %s is required by service %s", name, definition.Name)
			}
		}
	}
	if !found {
		return fmt.Errorf("service %s is not registered", name)
	}
	return s.Delete(name)
}

func (s Store) List() ([]Definition, error) {
	if err := s.prepare(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Directory)
	if err != nil {
		return nil, fmt.Errorf("list service definitions: %w", err)
	}
	definitions := make([]Definition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		definition, readErr := readDefinition(filepath.Join(s.Directory, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}
		if filepath.Base(s.definitionPath(definition.Name)) != entry.Name() {
			return nil, fmt.Errorf("service definition %s names %s", entry.Name(), definition.Name)
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	if _, err = Order(definitions); err != nil {
		return nil, err
	}
	return definitions, nil
}

func (s Store) definitionPath(name string) string {
	return filepath.Join(s.Directory, name+".json")
}

func (s Store) prepare() error {
	if s.Directory == "" || !filepath.IsAbs(s.Directory) {
		return errors.New("service store path must be absolute")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return fmt.Errorf("create service store: %w", err)
	}
	info, err := os.Lstat(s.Directory)
	if err != nil {
		return fmt.Errorf("inspect service store: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("service store is not a directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		if err = os.Chmod(s.Directory, 0700); err != nil {
			return fmt.Errorf("secure service store: %w", err)
		}
	}
	return nil
}

func readDefinition(path string) (Definition, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Definition{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Definition{}, fmt.Errorf("service definition %s is not a private regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return Definition{}, fmt.Errorf("open service definition: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1024*1024))
	decoder.DisallowUnknownFields()
	var definition Definition
	if err = decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("decode service definition %s: %w", path, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Definition{}, fmt.Errorf("decode service definition %s: %w", path, err)
	}
	if err = definition.Validate(); err != nil {
		return Definition{}, fmt.Errorf("invalid service definition %s: %w", path, err)
	}
	return definition, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func Order(definitions []Definition) ([]string, error) {
	known := make(map[string]Definition, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return nil, err
		}
		if _, exists := known[definition.Name]; exists {
			return nil, fmt.Errorf("service %s is defined more than once", definition.Name)
		}
		known[definition.Name] = definition
	}
	for _, definition := range definitions {
		for _, dependency := range definition.DependsOn {
			if _, exists := known[dependency]; !exists {
				return nil, fmt.Errorf("service %s depends on unknown service %s", definition.Name, dependency)
			}
		}
	}
	state := make(map[string]int, len(definitions))
	order := make([]string, 0, len(definitions))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("service dependency cycle at %s", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, dependency := range known[name].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
