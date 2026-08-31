/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const environmentSizeLimit = 1 << 20

func environmentInstance(id string) string {
	return "environment-" + id
}

func environmentDataID(id string) string {
	return "environment-" + id
}

func environmentContainerScope(id string) string {
	return "environment:" + id
}

func (c *Cpak) environmentsPath() string {
	return c.GetInStoreDir("environments")
}

func (c *Cpak) environmentPath(id string) (string, error) {
	if err := validateEnvironmentID(id); err != nil {
		return "", fmt.Errorf("invalid environment ID %q", id)
	}
	return filepath.Join(c.environmentsPath(), id), nil
}

func validateEnvironmentID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return errors.New("environment ID is invalid")
	}
	return nil
}

func validateEnvironmentName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 80 {
		return errors.New("environment name must contain 1-80 trimmed characters")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return errors.New("environment name cannot contain control characters")
		}
	}
	return nil
}

func validateEnvironmentPolicy(policy *types.Override) error {
	*policy = policy.WithMigratedFilesystem()
	if err := types.ValidateFilesystemPermissions(policy.Filesystem); err != nil {
		return err
	}
	if err := migrateLegacyHostCommands(policy); err != nil {
		return err
	}
	if err := types.ValidateHostActions(policy.HostActions); err != nil {
		return err
	}
	if err := types.ValidateFilePickerGrant(policy.FilePicker); err != nil {
		return err
	}
	if err := types.ValidateNetworkPermissions(*policy); err != nil {
		return err
	}
	if err := types.ValidateDBusPolicy(policy.SessionBus); err != nil {
		return err
	}
	if policy.MemoryMaxMB < 0 || policy.CPUQuota < 0 || policy.CPUQuota > 1000 || policy.PidsMax < 0 {
		return errors.New("environment resource limits are invalid")
	}
	policy.Filesystem = intersectFilesystem(policy.Filesystem, policy.Filesystem)
	policy.HostActions = types.IntersectHostActions(policy.HostActions, policy.HostActions)
	policy.SessionBus = types.CanonicalDBusPolicy(policy.SessionBus)
	return nil
}

func validateEnvironment(environment *types.Environment) error {
	if err := validateEnvironmentID(environment.ID); err != nil {
		return err
	}
	if err := validateEnvironmentName(environment.Name); err != nil {
		return err
	}
	if environment.ApplicationCpakId == "" || strings.ContainsAny(environment.ApplicationCpakId, "/\\\x00") {
		return errors.New("environment application ID is invalid")
	}
	if environment.Origin == "" || environment.Version == "" || environment.CreatedAt.IsZero() || environment.UpdatedAt.IsZero() {
		return errors.New("environment metadata is incomplete")
	}
	return validateEnvironmentPolicy(&environment.Policy)
}

func (c *Cpak) CreateEnvironment(name, origin, version, branch, commit, release string) (types.Environment, error) {
	if err := validateEnvironmentName(name); err != nil {
		return types.Environment{}, err
	}
	unlock, err := c.lockContainerScope("environment-names")
	if err != nil {
		return types.Environment{}, err
	}
	defer unlock()
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return types.Environment{}, err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil {
		return types.Environment{}, fmt.Errorf("find installed package %s: %w", origin, err)
	}
	if app.CpakId == "" {
		return types.Environment{}, fmt.Errorf("installed package not found: %s", origin)
	}
	policy, err := standaloneLaunchPolicy(store, app)
	if err != nil {
		return types.Environment{}, err
	}
	existing, err := c.ListEnvironments()
	if err != nil {
		return types.Environment{}, err
	}
	for _, environment := range existing {
		if strings.EqualFold(environment.Name, name) {
			return types.Environment{}, fmt.Errorf("environment name %q is already in use", name)
		}
	}
	now := time.Now().UTC()
	environment := types.Environment{
		ID:                uuid.NewString(),
		Name:              name,
		ApplicationCpakId: app.CpakId,
		Origin:            app.Origin,
		Version:           app.Version,
		Branch:            app.Branch,
		Commit:            app.Commit,
		Release:           app.Release,
		Policy:            policy.effective.WithMigratedFilesystem(),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err = c.writeEnvironment(environment); err != nil {
		if directory, pathErr := c.environmentPath(environment.ID); pathErr == nil {
			_ = os.RemoveAll(directory)
		}
		return types.Environment{}, err
	}
	return environment, nil
}

func (c *Cpak) ListEnvironments() ([]types.Environment, error) {
	root := c.environmentsPath()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []types.Environment{}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = provePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("environment storage is not private: %w", err)
	}
	result := make([]types.Environment, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		environment, readErr := c.loadEnvironment(entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, environment)
	}
	sort.Slice(result, func(left, right int) bool {
		if strings.EqualFold(result[left].Name, result[right].Name) {
			return result[left].ID < result[right].ID
		}
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	return result, nil
}

func (c *Cpak) GetEnvironment(value string) (types.Environment, error) {
	if parsed, err := uuid.Parse(value); err == nil && parsed.String() == value {
		return c.loadEnvironment(value)
	}
	environments, err := c.ListEnvironments()
	if err != nil {
		return types.Environment{}, err
	}
	for _, environment := range environments {
		if strings.EqualFold(environment.Name, value) {
			return environment, nil
		}
	}
	return types.Environment{}, fmt.Errorf("environment not found: %s", value)
}

func (c *Cpak) loadEnvironment(id string) (types.Environment, error) {
	directory, err := c.environmentPath(id)
	if err != nil {
		return types.Environment{}, err
	}
	if err = provePrivateDirectory(directory); err != nil {
		return types.Environment{}, fmt.Errorf("environment %s is not private: %w", id, err)
	}
	path := filepath.Join(directory, "environment.json")
	info, err := os.Lstat(path)
	if err != nil {
		return types.Environment{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return types.Environment{}, fmt.Errorf("environment metadata %s is not a private regular file", path)
	}
	if info.Size() > environmentSizeLimit {
		return types.Environment{}, fmt.Errorf("environment metadata %s exceeds %d bytes", path, environmentSizeLimit)
	}
	file, err := os.Open(path)
	if err != nil {
		return types.Environment{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, environmentSizeLimit))
	decoder.DisallowUnknownFields()
	environment := types.Environment{}
	if err = decoder.Decode(&environment); err != nil {
		return types.Environment{}, fmt.Errorf("decode environment %s: %w", id, err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return types.Environment{}, fmt.Errorf("decode environment %s: multiple JSON values", id)
	}
	if environment.ID != id {
		return types.Environment{}, fmt.Errorf("environment %s metadata names %s", id, environment.ID)
	}
	if err = validateEnvironment(&environment); err != nil {
		return types.Environment{}, fmt.Errorf("validate environment %s: %w", id, err)
	}
	return environment, nil
}

func (c *Cpak) writeEnvironment(environment types.Environment) error {
	if err := validateEnvironment(&environment); err != nil {
		return err
	}
	directory, err := c.environmentPath(environment.ID)
	if err != nil {
		return err
	}
	if err = securePrivateDirectoryUnder(c.Options.StorePath, directory); err != nil {
		return fmt.Errorf("prepare environment storage: %w", err)
	}
	data, err := json.MarshalIndent(environment, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > environmentSizeLimit {
		return fmt.Errorf("environment metadata exceeds %d bytes", environmentSizeLimit)
	}
	temporary, err := os.CreateTemp(directory, ".environment-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, filepath.Join(directory, "environment.json")); err != nil {
		return err
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func (c *Cpak) environmentPersistentState(environment types.Environment) (*persistentContainerState, error) {
	directory, err := c.environmentPath(environment.ID)
	if err != nil {
		return nil, err
	}
	upperDir := filepath.Join(directory, "root", "up")
	workDir := filepath.Join(directory, "root", "work")
	if err = securePrivateDirectoryUnder(c.Options.StorePath, upperDir); err != nil {
		return nil, err
	}
	if err = securePrivateDirectoryUnder(c.Options.StorePath, workDir); err != nil {
		return nil, err
	}
	return &persistentContainerState{
		scope:        environmentContainerScope(environment.ID),
		upperDir:     upperDir,
		workDir:      workDir,
		dataID:       environmentDataID(environment.ID),
		mapSystemIDs: true,
	}, nil
}

func (c *Cpak) RunEnvironment(value, binary string, verbose bool, arguments ...string) error {
	isVerbose = verbose
	if _, nested := getNested(); nested {
		return errors.New("an environment cannot be launched from another package")
	}
	if err := c.prepareSocketListener(); err != nil {
		return err
	}
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return err
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	app, changed, err := c.environmentApplication(store, environment)
	if err != nil {
		_ = store.Close()
		return err
	}
	if changed {
		environment.ApplicationCpakId = app.CpakId
		environment.Version = app.Version
		environment.Branch = app.Branch
		environment.Commit = app.Commit
		environment.Release = app.Release
		environment.UpdatedAt = time.Now().UTC()
		if err = c.writeEnvironment(environment); err != nil {
			_ = store.Close()
			return err
		}
	}
	policy, err := standaloneLaunchPolicy(store, app)
	if err != nil {
		_ = store.Close()
		return err
	}
	policy.effective = intersectOverrides(policy.effective.WithMigratedFilesystem(), environment.Policy.WithMigratedFilesystem())
	persistent, err := c.environmentPersistentState(environment)
	if err != nil {
		_ = store.Close()
		return err
	}
	persistent.refreshPolicy = func(current launchPolicy) (launchPolicy, error) {
		latest, loadErr := c.loadEnvironment(environment.ID)
		if loadErr != nil {
			return launchPolicy{}, loadErr
		}
		current.effective = intersectOverrides(current.effective.WithMigratedFilesystem(), latest.Policy.WithMigratedFilesystem())
		return current, nil
	}
	return c.runApplicationInstanceWithStoreAndState(app, policy, environmentInstance(environment.ID), binary, verbose, false, store, persistent, arguments...)
}

func (c *Cpak) StopEnvironment(value string) error {
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return err
	}
	scope := environmentContainerScope(environment.ID)
	unlock, err := c.lockContainerScope(scope)
	if err != nil {
		return err
	}
	defer unlock()
	return c.stopEnvironmentContainers(environment)
}

func (c *Cpak) stopEnvironmentContainers(environment types.Environment) error {
	return c.StopContainer(types.Application{CpakId: environmentContainerScope(environment.ID)})
}

func (c *Cpak) DeleteEnvironment(value string) error {
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return err
	}
	scope := environmentContainerScope(environment.ID)
	unlock, err := c.lockContainerScope(scope)
	if err != nil {
		return err
	}
	defer unlock()
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	containers, err := store.GetApplicationContainers(types.Application{CpakId: scope})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, container := range containers {
		terminateContainerProcess(container)
		if err = c.CleanupContainer(container); err != nil {
			return err
		}
	}
	if err = c.removeAllEnvironmentApplicationExports(environment); err != nil {
		return err
	}
	dataPath, err := c.applicationDataPath(environmentDataID(environment.ID))
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dataPath); err != nil {
		return err
	}
	directory, err := c.environmentPath(environment.ID)
	if err != nil {
		return err
	}
	if err = provePrivateDirectory(directory); err != nil {
		return err
	}
	return os.RemoveAll(directory)
}

func (c *Cpak) SetEnvironmentPolicy(value string, candidate types.Override) (types.Environment, error) {
	if err := validateEnvironmentPolicy(&candidate); err != nil {
		return types.Environment{}, err
	}
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return types.Environment{}, err
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return types.Environment{}, err
	}
	app, changed, err := c.environmentApplication(store, environment)
	if err != nil {
		_ = store.Close()
		return types.Environment{}, err
	}
	policy, err := standaloneLaunchPolicy(store, app)
	_ = store.Close()
	if err != nil {
		return types.Environment{}, err
	}
	if changed {
		environment.ApplicationCpakId = app.CpakId
		environment.Version = app.Version
		environment.Branch = app.Branch
		environment.Commit = app.Commit
		environment.Release = app.Release
	}
	scope := environmentContainerScope(environment.ID)
	unlock, err := c.lockContainerScope(scope)
	if err != nil {
		return types.Environment{}, err
	}
	defer unlock()
	latest, err := c.loadEnvironment(environment.ID)
	if err != nil {
		return types.Environment{}, err
	}
	latest.ApplicationCpakId = environment.ApplicationCpakId
	latest.Version = environment.Version
	latest.Branch = environment.Branch
	latest.Commit = environment.Commit
	latest.Release = environment.Release
	environment = latest
	ceiling := policy.effective.WithMigratedFilesystem()
	restricted := intersectOverrides(ceiling, candidate)
	want, _ := json.Marshal(candidate)
	got, _ := json.Marshal(restricted)
	if !bytes.Equal(want, got) {
		return types.Environment{}, errors.New("environment policy cannot grant permissions the installed package does not have")
	}
	environment.Policy = candidate
	environment.UpdatedAt = time.Now().UTC()
	if err = c.writeEnvironment(environment); err != nil {
		return types.Environment{}, err
	}
	if err = c.stopEnvironmentContainers(environment); err != nil {
		return types.Environment{}, err
	}
	return environment, nil
}

func (c *Cpak) EnvironmentPermissionCeiling(value string) (types.Override, error) {
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return types.Override{}, err
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return types.Override{}, err
	}
	defer store.Close()
	app, _, err := c.environmentApplication(store, environment)
	if err != nil {
		return types.Override{}, err
	}
	policy, err := standaloneLaunchPolicy(store, app)
	if err != nil {
		return types.Override{}, err
	}
	return policy.effective.WithMigratedFilesystem(), nil
}

func (c *Cpak) environmentApplication(store *Store, environment types.Environment) (types.Application, bool, error) {
	app, err := store.GetApplicationByCpakId(environment.ApplicationCpakId)
	if err == nil && app.CpakId != "" {
		return app, false, nil
	}
	applications, lookupErr := store.GetApplicationsByOrigin(environment.Origin, "", environment.Branch, "", "")
	if lookupErr != nil {
		return types.Application{}, false, lookupErr
	}
	if len(applications) == 0 && environment.Branch != "" {
		applications, lookupErr = store.GetApplicationsByOrigin(environment.Origin, "", "", "", "")
		if lookupErr != nil {
			return types.Application{}, false, lookupErr
		}
	}
	if len(applications) == 0 {
		return types.Application{}, false, fmt.Errorf("environment package is not installed: %s", environment.Origin)
	}
	if len(applications) > 1 {
		return types.Application{}, false, fmt.Errorf("environment package is ambiguous after an update: %s", environment.Origin)
	}
	return applications[0], true, nil
}

func (c *Cpak) environmentContainer(value string) (types.Environment, types.Container, error) {
	environment, err := c.GetEnvironment(value)
	if err != nil {
		return types.Environment{}, types.Container{}, err
	}
	scope := environmentContainerScope(environment.ID)
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return types.Environment{}, types.Container{}, err
	}
	containers, err := store.GetApplicationContainers(types.Application{CpakId: scope})
	_ = store.Close()
	if err != nil {
		return types.Environment{}, types.Container{}, err
	}
	for _, container := range containers {
		if containerProcessRunning(container) {
			return environment, container, nil
		}
	}
	return types.Environment{}, types.Container{}, errors.New("environment is not running")
}
