/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-slipstream/pkg/engine"
	"github.com/mirkobrombin/go-slipstream/pkg/wal"
)

type Store struct {
	Apps       *engine.Bitcask[types.Application]
	Containers *engine.Bitcask[types.Container]
}

const storeLockTimeout = 5 * time.Second

func openWAL(dir string, timeout time.Duration) (*wal.Manager, error) {
	deadline := time.Now().Add(timeout)
	for {
		manager, err := wal.NewManager(dir)
		if err == nil {
			return manager, nil
		}
		if !errors.Is(err, wal.ErrDirectoryLocked) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func NewStore(storePath string) (s *Store, err error) {
	appsDir := filepath.Join(storePath, "db", "apps")
	containersDir := filepath.Join(storePath, "db", "containers")

	// The database names every application and container on the machine, so
	// it is created as private as the tree it sits in rather than waiting for
	// an audit to notice.
	if err := securePrivateDirectoryUnder(storePath, appsDir); err != nil {
		return nil, err
	}
	if err := securePrivateDirectoryUnder(storePath, containersDir); err != nil {
		return nil, err
	}

	appCodec := func(app types.Application) ([]byte, error) { return json.Marshal(app) }
	appDecoder := func(data []byte) (types.Application, error) {
		var app types.Application
		err := json.Unmarshal(data, &app)
		return app, err
	}

	appWal, err := openWAL(appsDir, storeLockTimeout)
	if err != nil {
		return nil, err
	}
	apps := engine.NewBitcask(appWal, appCodec, appDecoder)
	apps.AddIndex("origin", func(app types.Application) string { return app.Origin })
	apps.AddIndex("version", func(app types.Application) string { return app.Version })
	if err := apps.Engine().Recover(); err != nil {
		logger.Errorf("Store: failed to recover apps index: %v", err)
	}

	containerCodec := func(c types.Container) ([]byte, error) { return json.Marshal(c) }
	containerDecoder := func(data []byte) (types.Container, error) {
		var c types.Container
		err := json.Unmarshal(data, &c)
		return c, err
	}

	containerWal, err := openWAL(containersDir, storeLockTimeout)
	if err != nil {
		_ = apps.Close()
		return nil, err
	}
	containers := engine.NewBitcask(containerWal, containerCodec, containerDecoder)
	containers.AddIndex("application_cpak_id", func(c types.Container) string { return c.ApplicationCpakId })
	if err := containers.Engine().Recover(); err != nil {
		logger.Errorf("Store: failed to recover containers index: %v", err)
	}

	s = &Store{
		Apps:       apps,
		Containers: containers,
	}

	return s, nil
}

func (s *Store) NewApplication(app types.Application) (err error) {
	if app.CpakId == "" {
		return errors.New("application CpakId is mandatory")
	}
	if app.InstallTimestamp.IsZero() {
		app.InstallTimestamp = time.Now()
	}
	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now()
	}

	return s.Apps.Put(context.Background(), app.CpakId, app, 0)
}

func (s *Store) NewContainer(container types.Container) (err error) {
	if container.CpakId == "" || container.ApplicationCpakId == "" {
		return errors.New("container CpakId and ApplicationCpakId are required")
	}
	if container.CreateTimestamp.IsZero() {
		container.CreateTimestamp = time.Now()
	}
	if container.CreatedAt.IsZero() {
		container.CreatedAt = time.Now()
	}

	return s.Containers.Put(context.Background(), container.CpakId, container, 0)
}

func (s *Store) GetApplications() (apps []types.Application, err error) {
	err = s.Apps.Engine().ForEach(func(key string, val types.Application) error {
		apps = append(apps, val)
		return nil
	})
	return apps, err
}

func (s *Store) GetApplicationByCpakId(cpakId string) (app types.Application, err error) {
	app, err = s.Apps.Get(context.Background(), cpakId)
	return app, err
}

func (s *Store) GetApplicationsByOrigin(origin, version string, branch string, commit string, release string) (apps []types.Application, err error) {
	res := s.Apps.GetByIndex(context.Background(), "origin", origin)
	apps, err = res.All()
	if err != nil {
		return nil, err
	}

	filtered := []types.Application{}
	for _, app := range apps {
		if version != "" && app.Version != version {
			continue
		}
		if branch != "" && app.Branch != branch {
			continue
		}
		if commit != "" && app.Commit != commit {
			continue
		}
		if release != "" && app.Release != release {
			continue
		}
		filtered = append(filtered, app)
	}

	return filtered, nil
}

func (s *Store) GetApplicationContainers(application types.Application) (containers []types.Container, err error) {
	if application.CpakId == "" {
		return nil, errors.New("application CpakId is required to get containers")
	}
	res := s.Containers.GetByIndex(context.Background(), "application_cpak_id", application.CpakId)
	return res.All()
}

func (s *Store) RemoveApplicationByCpakId(cpakId string) (err error) {
	return s.Apps.Delete(context.Background(), cpakId)
}

func (s *Store) RemoveContainerByCpakId(cpakId string) (err error) {
	return s.Containers.Delete(context.Background(), cpakId)
}

func (s *Store) SetContainerPid(cpakId string, pid int) (err error) {
	container, err := s.Containers.Get(context.Background(), cpakId)
	if err != nil {
		return err
	}
	container.Pid = pid
	return s.Containers.Put(context.Background(), cpakId, container, 0)
}

func (s *Store) SetContainerRuntime(cpakId string, pid int, cgroupPath string) error {
	container, err := s.Containers.Get(context.Background(), cpakId)
	if err != nil {
		return err
	}
	container.Pid = pid
	container.CgroupPath = cgroupPath
	return s.Containers.Put(context.Background(), cpakId, container, 0)
}

func (s *Store) RemoveApplicationByOriginAndBranch(origin, branch string) error {
	apps, err := s.GetApplicationsByOrigin(origin, "", branch, "", "")
	if err != nil {
		return err
	}
	for _, app := range apps {
		if err := s.RemoveApplicationByCpakId(app.CpakId); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndCommit(origin, commit string) error {
	apps, err := s.GetApplicationsByOrigin(origin, "", "", commit, "")
	if err != nil {
		return err
	}
	for _, app := range apps {
		if err := s.RemoveApplicationByCpakId(app.CpakId); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndRelease(origin, release string) error {
	apps, err := s.GetApplicationsByOrigin(origin, "", "", "", release)
	if err != nil {
		return err
	}
	for _, app := range apps {
		if err := s.RemoveApplicationByCpakId(app.CpakId); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	err1 := s.Apps.Close()
	err2 := s.Containers.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (s *Store) GetApplicationByOrigin(origin, version string, branch string, commit string, release string) (app types.Application, err error) {
	apps, err := s.GetApplicationsByOrigin(origin, version, branch, commit, release)
	if err != nil {
		return app, err
	}
	if len(apps) > 0 {
		return apps[0], nil
	}
	return app, fmt.Errorf("application not found: %s", origin)
}

func (s *Store) GetApplicationByDesktopEntry(desktopEntry string) (app types.Application, err error) {
	err = s.Apps.Engine().ForEach(func(key string, val types.Application) error {
		for _, de := range val.ParsedDesktopEntries {
			if de == desktopEntry {
				app = val
				return errors.New("found")
			}
		}
		return nil
	})
	if err != nil && err.Error() == "found" {
		return app, nil
	}
	return app, errors.New("not found")
}

func (s *Store) GetApplicationByBinary(binary string) (app types.Application, err error) {
	err = s.Apps.Engine().ForEach(func(key string, val types.Application) error {
		for _, b := range val.ParsedBinaries {
			if b == binary {
				app = val
				return errors.New("found")
			}
		}
		return nil
	})
	if err != nil && err.Error() == "found" {
		return app, nil
	}
	return app, errors.New("not found")
}

func (s *Store) ParseDependenciesString(dependencyCpakIdsString string) (deps []types.Dependency, err error) {
	if dependencyCpakIdsString == "" {
		return []types.Dependency{}, nil
	}
	ids := strings.Split(dependencyCpakIdsString, ",")
	for _, idStr := range ids {
		if idStr == "" {
			continue
		}
		app, getErr := s.GetApplicationByCpakId(idStr)
		if getErr == nil {
			deps = append(deps, types.Dependency{
				Id:      app.CpakId,
				Branch:  app.Branch,
				Release: app.Release,
				Commit:  app.Commit,
				Origin:  app.Origin,
			})
		}
	}
	return deps, nil
}
