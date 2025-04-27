package cpak

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type Store struct {
	db *sql.DB
}

// NewStore creates a new Store instance.
func NewStore(dbPath string) (s *Store, err error) {
	dbPath = dbPath + "/cpak.db"
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return
	}

	s = &Store{db: db}

	err = s.initDb(dbPath)
	if err != nil {
		return
	}

	return
}

// isDbInitialized checks if the database is initialized or not.
func (s *Store) isDbInitialized() bool {
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name='Application'")
	if err != nil {
		return false
	}
	defer rows.Close()

	return rows.Next()
}

// initDb initializes the database if not already done.
func (s *Store) initDb(dbPath string) (err error) {
	if s.isDbInitialized() {
		return
	}

	// Application table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS Application (
			Id TEXT PRIMARY KEY UNIQUE,
			Name TEXT,
			Version TEXT,
			Branch TEXT,
			"Commit" TEXT,
			Release TEXT,
			Origin TEXT,
			Timestamp DATETIME,
			Binaries TEXT,
			DesktopEntries TEXT,
			Dependencies TEXT,
			Addons TEXT,
			Layers TEXT,
			Config TEXT,
			Override TEXT
		)
	`)

	if err != nil {
		return fmt.Errorf("initDb: %s", err)
	}

	// Container table
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS Container (
			Id TEXT PRIMARY KEY,
			Pid INTEGER,
			ApplicationId TEXT,
			Timestamp DATETIME,
			StatePath TEXT,
			HostExecPid INTEGER,
			HostExecSocketPath TEXT,
			FOREIGN KEY(ApplicationId) REFERENCES Application(Id)
		)
	`)

	if err != nil {
		return fmt.Errorf("initDb: %s", err)
	}

	return nil
}

// NewApplication inserts a new application into the store.
func (s *Store) NewApplication(app types.Application) (err error) {
	binaries := strings.Join(app.Binaries, ",")
	desktopEntries := strings.Join(app.DesktopEntries, ",")
	dependenciesList := []string{}
	for _, dependency := range app.Dependencies {
		dependenciesList = append(dependenciesList, dependency.Id)
	}
	dependencies := strings.Join(dependenciesList, ",")
	addons := strings.Join(app.Addons, ",")
	layers := strings.Join(app.Layers, ",")
	override := StringOverride(app.Override)

	_, err = s.db.Exec(
		"INSERT INTO Application VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		app.Id, app.Name, app.Version, app.Branch, app.Commit, app.Release, app.Origin, app.Timestamp, binaries, desktopEntries, dependencies, addons, layers, app.Config, override,
	)
	if err != nil {
		err = fmt.Errorf("NewApplication: %s", err)
		return
	}

	return
}

// NewContainer inserts a new container into the store.
func (s *Store) NewContainer(container types.Container) (err error) {
	if container.Id == "" || container.Application.Id == "" {
		return errors.New("container Id and ApplicationId are required")
	}
	if container.Timestamp.IsZero() {
		container.Timestamp = time.Now()
	}

	_, err = s.db.Exec(
		"INSERT INTO Container (Id, Pid, ApplicationId, Timestamp, StatePath, HostExecPid, HostExecSocketPath) VALUES (?, ?, ?, ?, ?, ?, ?)",
		container.Id, container.Pid, container.Application.Id, container.Timestamp, container.StatePath, container.HostExecPid, container.HostExecSocketPath,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: Container.Id") {
			return fmt.Errorf("container with Id %s already exists: %w", container.Id, err)
		}
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return fmt.Errorf("application with Id %s does not exist: %w", container.Application.Id, err)
		}
		return fmt.Errorf("failed to insert container %s: %w", container.Id, err)
	}

	return nil
}

// GetApplications returns all the applications stored in the store.
func (s *Store) GetApplications() (apps []types.Application, err error) {
	rows, err := s.db.Query("SELECT * FROM Application ORDER BY Timestamp DESC")
	if err != nil {
		err = fmt.Errorf("GetApplications: %s", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var app types.Application
		var desktopEntries string
		var dependencies string
		var addons string
		var binaries string
		var layers string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &dependencies, &addons, &layers, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplications: %s", err)
			return
		}
		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.Dependencies, err = s.ParseDependencies(dependencies)
		if err != nil {
			err = fmt.Errorf("GetApplicationContainers: %s", err)
			return
		}
		app.Addons = strings.Split(addons, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Layers = strings.Split(layers, ",")
		app.Override = ParseOverride(override)
		apps = append(apps, app)
	}

	return
}

// GetApplicationById returns an Application instance based on its Id.
func (s *Store) GetApplicationById(id string) (app types.Application, err error) {
	rows, err := s.db.Query("SELECT * FROM Application WHERE Id = ?", id)
	if err != nil {
		err = fmt.Errorf("GetApplicationById: %s", err)
		return
	}
	defer rows.Close()

	if rows.Next() {
		var desktopEntries string
		var dependencies string
		var addons string
		var binaries string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &dependencies, &addons, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationById: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.Dependencies, err = s.ParseDependencies(dependencies)
		if err != nil {
			err = fmt.Errorf("GetApplicationContainers: %s", err)
			return
		}
		app.Addons = strings.Split(addons, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Override = ParseOverride(override)
	} else {
		err = errors.New("application not found")
	}

	return
}

// GetApplicationsByOrigin returns an Application instance based on its Origin.
// It accepts an optional version parameter.
func (s *Store) GetApplicationsByOrigin(origin, version string, branch string, commit string, release string) (apps []types.Application, err error) {
	var rows *sql.Rows
	if version != "" {
		rows, err = s.db.Query("SELECT * FROM Application WHERE Origin = ? AND Version = ? AND Branch = ? AND \"Commit\" = ? AND Release = ? ORDER BY Timestamp DESC", origin, version, branch, commit, release)
	} else {
		rows, err = s.db.Query("SELECT * FROM Application WHERE Origin = ? ORDER BY Timestamp DESC", origin)
	}
	if err != nil {
		err = fmt.Errorf("GetApplicationsByOrigin: %s", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var app types.Application
		var desktopEntries string
		var dependencies string
		var addons string
		var binaries string
		var layers string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &dependencies, &addons, &layers, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationsByOrigin: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.Dependencies, err = s.ParseDependencies(dependencies)
		if err != nil {
			err = fmt.Errorf("GetApplicationContainers: %s", err)
			return
		}
		app.Addons = strings.Split(addons, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Layers = strings.Split(layers, ",")
		app.Override = ParseOverride(override)
		apps = append(apps, app)
	}

	return
}

// GetApplicationsByAddons returns an Application instance based on its Addons.
func (s *Store) GetApplicationsByAddons(dependencies []string) (apps []types.Application, err error) {
	rows, err := s.db.Query("SELECT * FROM Application WHERE Addons = ? ORDER BY Timestamp DESC", strings.Join(dependencies, ","))
	if err != nil {
		err = fmt.Errorf("GetApplicationsByAddons: %s", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var app types.Application
		var desktopEntries string
		var dependencies string
		var addons string
		var binaries string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &dependencies, &addons, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationsByAddons: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.Dependencies, err = s.ParseDependencies(dependencies)
		if err != nil {
			err = fmt.Errorf("GetApplicationContainers: %s", err)
			return
		}
		app.Addons = strings.Split(addons, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Override = ParseOverride(override)
		apps = append(apps, app)
	}

	return
}

// GetApplicationContainers returns the containers associated with a specific application.
func (s *Store) GetApplicationContainers(application types.Application) (containers []types.Container, err error) {
	if application.Id == "" {
		return nil, errors.New("application ID is required to get containers")
	}

	rows, err := s.db.Query("SELECT * FROM Container WHERE ApplicationId = ? ORDER BY Timestamp DESC", application.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to query containers for app %s: %w", application.Id, err)
	}
	defer rows.Close()

	containers = []types.Container{}
	for rows.Next() {
		container, scanErr := scanContainer(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("failed to scan container row for app %s: %w", application.Id, scanErr)
		}
		container.Application = application
		containers = append(containers, container)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating container rows for app %s: %w", application.Id, err)
	}

	return containers, nil
}

// RemoveApplicationById removes an application based on the ID provided as a parameter.
func (s *Store) RemoveApplicationById(id string) (err error) {
	_, err = s.db.Exec("DELETE FROM Application WHERE Id = ?", id)
	if err != nil {
		err = fmt.Errorf("RemoveApplicationById: %s", err)
		return
	}

	return
}

// RemoveApplicationByOriginAndVersion removes an application based on the Origin and Version provided as parameters.
func (s *Store) RemoveApplicationByOriginAndVersion(origin, version string) (err error) {
	_, err = s.db.Exec("DELETE FROM Application WHERE Origin = ? AND Version = ?", origin, version)
	if err != nil {
		err = fmt.Errorf("RemoveApplicationByOriginAndVersion: %s", err)
		return
	}

	return
}

// RemoveApplicationByOriginAndBranch removes an application based on the Origin and Branch provided as parameters.
func (s *Store) RemoveApplicationByOriginAndBranch(origin, branch string) (err error) {
	_, err = s.db.Exec("DELETE FROM Application WHERE Origin = ? AND Branch = ?", origin, branch)
	if err != nil {
		err = fmt.Errorf("RemoveApplicationByOriginAndBranch: %s", err)
		return
	}

	return
}

// RemoveApplicationByOriginAndCommit removes an application based on the Origin and Commit provided as parameters.
func (s *Store) RemoveApplicationByOriginAndCommit(origin, commit string) (err error) {
	_, err = s.db.Exec("DELETE FROM Application WHERE Origin = ? AND \"Commit\" = ?", origin, commit)
	if err != nil {
		err = fmt.Errorf("RemoveApplicationByOriginAndCommit: %s", err)
		return
	}

	return
}

// RemoveApplicationByOriginAndRelease removes an application based on the Origin and Release provided as parameters.
func (s *Store) RemoveApplicationByOriginAndRelease(origin, release string) (err error) {
	_, err = s.db.Exec("DELETE FROM Application WHERE Origin = ? AND Release = ?", origin, release)
	if err != nil {
		err = fmt.Errorf("RemoveApplicationByOriginAndRelease: %s", err)
		return
	}

	return
}

// RemoveContainerById removes a container based on the ID provided as a parameter.
func (s *Store) RemoveContainerById(id string) (err error) {
	_, err = s.db.Exec("DELETE FROM Container WHERE Id = ?", id)
	if err != nil {
		err = fmt.Errorf("RemoveContainerById: %s", err)
		return
	}

	return
}

// SetContainerPid sets the PID of a container based on the ID provided as a parameter.
func (s *Store) SetContainerPid(id string, pid int) (err error) {
	_, err = s.db.Exec("UPDATE Container SET Pid = ? WHERE Id = ?", pid, id)
	if err != nil {
		err = fmt.Errorf("SetContainerPid: %s", err)
		return
	}

	return
}

// RemoveContainer removes a container based on the ID provided as a parameter.
func (s *Store) RemoveContainer(id string) (err error) {
	_, err = s.db.Exec("DELETE FROM Container WHERE Id = ?", id)
	if err != nil {
		err = fmt.Errorf("RemoveContainer: %s", err)
		return
	}

	return
}

// The following funcs are helpers for convenience.

// GetApplicationByOrigin returns an Application instance based on its Origin
// and Version.
func (s *Store) GetApplicationByOrigin(origin, version string, branch string, commit string, release string) (app types.Application, err error) {
	apps, err := s.GetApplicationsByOrigin(origin, version, branch, commit, release)
	if err != nil {
		err = fmt.Errorf("GetApplicationByOrigin: %s", err)
		return
	}

	if len(apps) > 0 {
		app = apps[0]
	}

	return
}

// GetApplicationByAddons returns an Application instance based on its Addons.
func (s *Store) GetApplicationByAddons(dependencies []string) (app types.Application, err error) {
	apps, err := s.GetApplicationsByAddons(dependencies)
	if err != nil {
		err = fmt.Errorf("GetApplicationByAddons: %s", err)
		return
	}

	if len(apps) > 0 {
		app = apps[0]
	}

	return
}

// GetApplicationByDesktopEntry returns an Application instance based on its DesktopEntry.
func (s *Store) GetApplicationByDesktopEntry(desktopEntry string) (app types.Application, err error) {
	apps, err := s.GetApplications()
	if err != nil {
		err = fmt.Errorf("GetApplicationByDesktopEntry: %s", err)
		return
	}

	for _, _app := range apps {
		for _, _desktopEntry := range _app.DesktopEntries {
			if _desktopEntry == desktopEntry {
				app = _app
				return
			}
		}
	}

	return
}

// GetApplicationByBinary returns an Application instance based on its Binary.
func (s *Store) GetApplicationByBinary(binary string) (app types.Application, err error) {
	apps, err := s.GetApplications()
	if err != nil {
		err = fmt.Errorf("GetApplicationByBinary: %s", err)
		return
	}

	for _, _app := range apps {
		for _, _binary := range _app.Binaries {
			if _binary == binary {
				app = _app
				return
			}
		}
	}

	return
}

// ParseDependencies parses a string of dependencies into a slice of Dependency.
//
// Note: dependencies are references to other cpaaks, so they are expected to be
// the id of the application.
func (s *Store) ParseDependencies(dependencies string) (deps []types.Dependency, err error) {
	for _, dependency := range strings.Split(dependencies, ",") {
		if dependency != "" {
			app, err := s.GetApplicationById(dependency)
			if err == nil {
				deps = append(deps, types.Dependency{
					Id:      app.Id,
					Branch:  app.Branch,
					Release: app.Release,
					Commit:  app.Commit,
					Origin:  app.Origin,
				})
			}
		}
	}

	return
}

// scanContainer scans a row into a Container struct (without full Application details initially).
func scanContainer(rows *sql.Rows) (types.Container, error) {
	var container types.Container
	err := rows.Scan(
		&container.Id, &container.Pid, &container.Application.Id,
		&container.Timestamp, &container.StatePath, &container.HostExecPid,
		&container.HostExecSocketPath,
	)
	return container, err
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
