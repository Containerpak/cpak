package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type Store struct {
	DB *gorm.DB
}

func NewStore(dbPath string) (s *Store, err error) {
	fullDbPath := dbPath + "/cpak.db"
	db, err := gorm.Open(sqlite.Open(fullDbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	s = &Store{DB: db}

	err = s.migrate()
	if err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return s, nil
}

func (s *Store) migrate() error {
	err := s.DB.AutoMigrate(&types.Application{}, &types.Container{})
	if err != nil {
		return fmt.Errorf("gorm automigrate: %w", err)
	}
	return nil
}

func (s *Store) serializeApplicationFields(app *types.Application) {
	app.Binaries = strings.Join(app.ParsedBinaries, ",")
	app.DesktopEntries = strings.Join(app.ParsedDesktopEntries, ",")

	depIds := []string{}
	for _, dep := range app.ParsedDependencies {
		depIds = append(depIds, dep.Id)
	}
	app.DependenciesRaw = strings.Join(depIds, ",")

	app.Addons = strings.Join(app.ParsedAddons, ",")
	app.Layers = strings.Join(app.ParsedLayers, ",")

	defaultOverride := types.NewOverride()
	if !reflect.DeepEqual(app.ParsedOverride, defaultOverride) {
		overrideBytes, _ := json.Marshal(app.ParsedOverride)
		app.OverrideRaw = string(overrideBytes)
	} else {
		app.OverrideRaw = ""
	}
}

func (s *Store) parseApplicationFields(app *types.Application) {
	if app.Binaries != "" {
		app.ParsedBinaries = strings.Split(app.Binaries, ",")
	} else {
		app.ParsedBinaries = []string{}
	}
	if app.DesktopEntries != "" {
		app.ParsedDesktopEntries = strings.Split(app.DesktopEntries, ",")
	} else {
		app.ParsedDesktopEntries = []string{}
	}

	if app.DependenciesRaw != "" {
		parsedDeps, _ := s.ParseDependenciesString(app.DependenciesRaw)
		app.ParsedDependencies = parsedDeps
	} else {
		app.ParsedDependencies = []types.Dependency{}
	}

	if app.Addons != "" {
		app.ParsedAddons = strings.Split(app.Addons, ",")
	} else {
		app.ParsedAddons = []string{}
	}
	if app.Layers != "" {
		app.ParsedLayers = strings.Split(app.Layers, ",")
	} else {
		app.ParsedLayers = []string{}
	}

	if app.OverrideRaw != "" && app.OverrideRaw != "{}" {
		json.Unmarshal([]byte(app.OverrideRaw), &app.ParsedOverride)
	} else {
		app.ParsedOverride = types.NewOverride()
	}
}

func (s *Store) NewApplication(app types.Application) (err error) {
	s.serializeApplicationFields(&app)

	if app.CpakId == "" {
		return errors.New("application CpakId is mandatory")
	}
	if app.InstallTimestamp.IsZero() {
		app.InstallTimestamp = time.Now()
	}

	result := s.DB.Create(&app)
	if result.Error != nil {
		return fmt.Errorf("NewApplication %w", result.Error)
	}
	return nil
}

func (s *Store) NewContainer(container types.Container) (err error) {
	if container.CpakId == "" || container.ApplicationCpakId == "" {
		return errors.New("container CpakId and ApplicationCpakId are required")
	}
	if container.CreateTimestamp.IsZero() {
		container.CreateTimestamp = time.Now()
	}

	result := s.DB.Create(&container)
	if result.Error != nil {
		if strings.Contains(result.Error.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("container with CpakId %s already exists: %w", container.CpakId, result.Error)
		}
		return fmt.Errorf("failed to insert container %s: %w", container.CpakId, result.Error)
	}
	return nil
}

func (s *Store) GetApplications() (apps []types.Application, err error) {
	result := s.DB.Order("install_timestamp desc").Find(&apps)
	if result.Error != nil {
		return nil, fmt.Errorf("GetApplications %w", result.Error)
	}
	for i := range apps {
		s.parseApplicationFields(&apps[i])
	}
	return apps, nil
}

func (s *Store) GetApplicationByCpakId(cpakId string) (app types.Application, err error) {
	result := s.DB.Where("cpak_id = ?", cpakId).First(&app)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return app, fmt.Errorf("application with cpak_id %s not found", cpakId)
		}
		return app, fmt.Errorf("GetApplicationByCpakId %w", result.Error)
	}
	s.parseApplicationFields(&app)
	return app, nil
}

func (s *Store) GetApplicationsByOrigin(origin, version string, branch string, commit string, release string) (apps []types.Application, err error) {
	query := s.DB.Where("origin = ?", origin)
	if version != "" {
		query = query.Where("version = ?", version)
	}
	if branch != "" {
		query = query.Where("branch = ?", branch)
	}
	if commit != "" {
		query = query.Where("commit = ?", commit)
	}
	if release != "" {
		query = query.Where("release = ?", release)
	}

	result := query.Order("install_timestamp desc").Find(&apps)
	if result.Error != nil {
		return nil, fmt.Errorf("GetApplicationsByOrigin %w", result.Error)
	}
	for i := range apps {
		s.parseApplicationFields(&apps[i])
	}
	return apps, nil
}

func (s *Store) GetApplicationContainers(application types.Application) (containers []types.Container, err error) {
	if application.CpakId == "" {
		return nil, errors.New("application CpakId is required to get containers")
	}
	result := s.DB.Where("application_cpak_id = ?", application.CpakId).Order("create_timestamp desc").Find(&containers)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to query containers for app %s: %w", application.CpakId, result.Error)
	}
	return containers, nil
}

func (s *Store) RemoveApplicationByCpakId(cpakId string) (err error) {
	result := s.DB.Unscoped().Where("cpak_id = ?", cpakId).Delete(&types.Application{})
	if result.Error != nil {
		return fmt.Errorf("RemoveApplicationByCpakId %w", result.Error)
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndVersion(origin, version string) (err error) {
	result := s.DB.Unscoped().Where("origin = ? AND version = ?", origin, version).Delete(&types.Application{})
	if result.Error != nil {
		return fmt.Errorf("RemoveApplicationByOriginAndVersion %w", result.Error)
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndBranch(origin, branch string) (err error) {
	result := s.DB.Unscoped().Where("origin = ? AND branch = ?", origin, branch).Delete(&types.Application{})
	if result.Error != nil {
		return fmt.Errorf("RemoveApplicationByOriginAndBranch %w", result.Error)
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndCommit(origin, commit string) (err error) {
	result := s.DB.Unscoped().Where("origin = ? AND commit = ?", origin, commit).Delete(&types.Application{})
	if result.Error != nil {
		return fmt.Errorf("RemoveApplicationByOriginAndCommit %w", result.Error)
	}
	return nil
}

func (s *Store) RemoveApplicationByOriginAndRelease(origin, release string) (err error) {
	result := s.DB.Unscoped().Where("origin = ? AND release = ?", origin, release).Delete(&types.Application{})
	if result.Error != nil {
		return fmt.Errorf("RemoveApplicationByOriginAndRelease %w", result.Error)
	}
	return nil
}

func (s *Store) RemoveContainerByCpakId(cpakId string) (err error) {
	result := s.DB.Unscoped().Where("cpak_id = ?", cpakId).Delete(&types.Container{})
	if result.Error != nil {
		return fmt.Errorf("RemoveContainerByCpakId %w", result.Error)
	}
	return nil
}

func (s *Store) SetContainerPid(cpakId string, pid int) (err error) {
	result := s.DB.Model(&types.Container{}).Where("cpak_id = ?", cpakId).Update("pid", pid)
	if result.Error != nil {
		return fmt.Errorf("SetContainerPid %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("no container found with cpak_id %s to update PID", cpakId)
	}
	return nil
}

func (s *Store) RemoveContainer(id string) (err error) {
	return s.RemoveContainerByCpakId(id)
}

func (s *Store) GetApplicationByOrigin(origin, version string, branch string, commit string, release string) (app types.Application, err error) {
	apps, err := s.GetApplicationsByOrigin(origin, version, branch, commit, release)
	if err != nil {
		return app, fmt.Errorf("GetApplicationByOrigin: %w", err)
	}
	if len(apps) > 0 {
		return apps[0], nil
	}
	return app, gorm.ErrRecordNotFound
}

func (s *Store) GetApplicationByAddons(addons []string) (app types.Application, err error) {
	return app, errors.New("GetApplicationByAddons not fully implemented with GORM for CSV field")
}

func (s *Store) GetApplicationByDesktopEntry(desktopEntry string) (app types.Application, err error) {
	apps, err := s.GetApplications()
	if err != nil {
		return app, fmt.Errorf("GetApplicationByDesktopEntry (loading apps): %w", err)
	}
	for _, currentApp := range apps {
		for _, de := range currentApp.ParsedDesktopEntries {
			if de == desktopEntry {
				return currentApp, nil
			}
		}
	}
	return app, gorm.ErrRecordNotFound
}

func (s *Store) GetApplicationByBinary(binary string) (app types.Application, err error) {
	apps, err := s.GetApplications()
	if err != nil {
		return app, fmt.Errorf("GetApplicationByBinary (loading apps): %w", err)
	}
	for _, currentApp := range apps {
		for _, bin := range currentApp.ParsedBinaries {
			if bin == binary {
				return currentApp, nil
			}
		}
	}
	return app, gorm.ErrRecordNotFound
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

func (s *Store) Close() error {
	if s.DB != nil {
		sqlDB, err := s.DB.DB()
		if err != nil {
			return fmt.Errorf("failed to get underlying sql.DB for closing: %w", err)
		}
		return sqlDB.Close()
	}
	return nil
}
