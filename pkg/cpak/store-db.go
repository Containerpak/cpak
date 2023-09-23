package cpak

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
			FutureDependencies TEXT,
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
	futureDependencies := strings.Join(app.FutureDependencies, ",")
	layers := strings.Join(app.Layers, ",")
	override := StringOverride(app.Override)

	_, err = s.db.Exec(
		"INSERT INTO Application VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		app.Id, app.Name, app.Version, app.Branch, app.Commit, app.Release, app.Origin, app.Timestamp, binaries, desktopEntries, futureDependencies, layers, app.Config, override,
	)
	if err != nil {
		err = fmt.Errorf("NewApplication: %s", err)
		return
	}

	return
}

// NewContainer inserts a new container into the store.
func (s *Store) NewContainer(container types.Container) (err error) {
	if container.Application.Id == "" {
		return errors.New("application id is required")
	}

	_, err = s.db.Exec(
		"INSERT INTO Container VALUES (?, ?, ?, ?)",
		container.Id, container.Pid, container.Application.Id, container.Timestamp,
	)
	if err != nil {
		err = fmt.Errorf("NewContainer: %s", err)
		return
	}

	return
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
		var futureDependencies string
		var binaries string
		var layers string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &futureDependencies, &layers, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplications: %s", err)
			return
		}
		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.FutureDependencies = strings.Split(futureDependencies, ",")
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
		var futureDependencies string
		var binaries string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &futureDependencies, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationById: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.FutureDependencies = strings.Split(futureDependencies, ",")
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
		var futureDependencies string
		var binaries string
		var layers string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Branch, &app.Commit, &app.Release, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &futureDependencies, &layers, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationsByOrigin: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.FutureDependencies = strings.Split(futureDependencies, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Layers = strings.Split(layers, ",")
		app.Override = ParseOverride(override)
		apps = append(apps, app)
	}

	return
}

// GetApplicationsByFutureDependencies returns an Application instance based on its FutureDependencies.
func (s *Store) GetApplicationsByFutureDependencies(dependencies []string) (apps []types.Application, err error) {
	rows, err := s.db.Query("SELECT * FROM Application WHERE FutureDependencies = ? ORDER BY Timestamp DESC", strings.Join(dependencies, ","))
	if err != nil {
		err = fmt.Errorf("GetApplicationsByFutureDependencies: %s", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var app types.Application
		var desktopEntries string
		var futureDependencies string
		var binaries string
		var override string
		err = rows.Scan(&app.Id, &app.Name, &app.Version, &app.Origin, &app.Timestamp, &binaries, &desktopEntries, &futureDependencies, &app.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationsByFutureDependencies: %s", err)
			return
		}

		app.DesktopEntries = strings.Split(desktopEntries, ",")
		app.FutureDependencies = strings.Split(futureDependencies, ",")
		app.Binaries = strings.Split(binaries, ",")
		app.Override = ParseOverride(override)
		apps = append(apps, app)
	}

	return
}

// GetApplicationContainers returns the containers associated with a specific application.
func (s *Store) GetApplicationContainers(application types.Application) (containers []types.Container, err error) {
	rows, err := s.db.Query("SELECT * FROM Container INNER JOIN Application ON Container.ApplicationId = Application.Id WHERE ApplicationId = ? ORDER BY Timestamp DESC", application.Id)
	if err != nil {
		err = fmt.Errorf("GetApplicationContainers: %s", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var container types.Container
		var desktopEntries string
		var futureDependencies string
		var binaries string
		var layers string
		var override string
		err = rows.Scan(&container.Id, &container.Pid, &container.Application.Id, &container.Timestamp, &container.Application.Id, &container.Application.Name, &container.Application.Version, &container.Application.Branch, &container.Application.Commit, &container.Application.Release, &container.Application.Origin, &container.Application.Timestamp, &binaries, &desktopEntries, &futureDependencies, &layers, &container.Application.Config, &override)
		if err != nil {
			err = fmt.Errorf("GetApplicationContainers: %s", err)
			return
		}

		container.Application.DesktopEntries = strings.Split(desktopEntries, ",")
		container.Application.FutureDependencies = strings.Split(futureDependencies, ",")
		container.Application.Binaries = strings.Split(binaries, ",")
		container.Application.Layers = strings.Split(layers, ",")
		container.Application.Override = ParseOverride(override)
		containers = append(containers, container)
	}

	return

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

// GetApplicationByFutureDependencies returns an Application instance based on its FutureDependencies.
func (s *Store) GetApplicationByFutureDependencies(dependencies []string) (app types.Application, err error) {
	apps, err := s.GetApplicationsByFutureDependencies(dependencies)
	if err != nil {
		err = fmt.Errorf("GetApplicationByFutureDependencies: %s", err)
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
