/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cpak

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/pkg/storage"
	"github.com/shirou/gopsutil/process"
)

type Cpak struct {
	Options types.CpakOptions
	Ctx     context.Context
}

// NewCpak creates a new cpak instance.
func NewCpak() (cpak Cpak, err error) {
	cpak.Options, err = getCpakOptions()
	if err != nil {
		return
	}

	cpak.Ctx = context.Background()
	return
}

// getCpakOptions reads cpak configuration options following a defined
// priority order:
//  1. If the CPAK_OPTS_FILE environment variable is set, the configuration
//     file path is extracted from this variable and used as the sole source.
//  2. Otherwise, configuration files are searched in three predefined
//     locations, in order:
//     a. In the current user's specific path: "~/.config/cpak/cpak.json".
//     b. In the system directory: "/etc/cpak/cpak.json".
//     c. In the cpak installation directory: "/usr/share/cpak/cpak.json".
//  3. If any configuration file is found, options are loaded from that file.
//  4. If no configuration file is found, or an error occurs during reading,
//     cpak searches for the installation path using the
//     CPAK_INSTALLATION_PATH environment variable. If this variable is not
//     set, the default installation path in the current user's directory
//     is used: "~/.local/share/cpak".
//  5. Necessary directories for cpak are then created, if they don't exist,
//     based on the installation path.
//  6. The function ensures that the system meets the required dependencies,
//     such as the presence of "rootlesskit" in the specified bin directory.
func getCpakOptions() (options types.CpakOptions, err error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	var confPaths []string
	var installationPath string

	// Try to read the options from the environment variable at first
	// if it's not set, try to read the options from the default paths
	if os.Getenv("CPAK_OPTS_FILE") != "" {
		confPaths = append(confPaths, os.Getenv("CPAK_OPTS_FILE"))
	} else {
		confPaths = append(confPaths, filepath.Join(homedir, ".config", "cpak", "cpak.json"))
		confPaths = append(confPaths, filepath.Join("/", "etc", "cpak", "cpak.json"))
		confPaths = append(confPaths, filepath.Join("/", "usr", "share", "cpak", "cpak.json"))
	}

	for _, confPath := range confPaths {
		if _, err = os.Stat(confPath); err == nil {
			options, err = readCpakOptions(confPath)
			break
		}
	}

	// If no options are found, look for the installation path
	// in the environment variable, otherwise use the default one
	if err != nil {
		if os.Getenv("CPAK_INSTALLATION_PATH") != "" {
			installationPath = os.Getenv("CPAK_INSTALLATION_PATH")
		} else {
			installationPath = filepath.Join(homedir, ".local", "share", "cpak")
		}

		options = types.CpakOptions{
			BinPath:       filepath.Join(installationPath, "bin"),
			ManifestsPath: filepath.Join(installationPath, "manifests"),
			ExportsPath:   filepath.Join(installationPath, "exports"),
			StorePath:     filepath.Join(installationPath, "store"),
			DaBaDeeStoreOptions: storage.StorageOptions{
				Root:         filepath.Join(installationPath, "dabadee"),
				WithMetadata: true,
			},
			CachePath: filepath.Join(installationPath, "cache"),
		}
	}

	// Other store paths are generated from the store path
	options.StoreLayersPath = filepath.Join(options.StorePath, "layers")
	options.StoreContainersPath = filepath.Join(options.StorePath, "containers")
	options.StoreStatesPath = filepath.Join(options.StorePath, "states")
	options.RotlesskitBinPath = filepath.Join(options.BinPath, "rootlesskit")
	options.NsenterBinPath = filepath.Join(options.BinPath, "nsenter")

	// Create the necessary directories if they don't exist
	err = createCpakDirs(&options)
	if err != nil {
		return
	}

	// Ensure the system meets the dependencies
	err = tools.EnsureUnixDeps(options.BinPath, "rootlesskit")
	if err != nil {
		return
	}

	return
}

// readCpakOptions reads and parses the configuration file at the given path.
// The file must be a valid JSON file.
func readCpakOptions(path string) (options types.CpakOptions, err error) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&options)
	return
}

// createCpakDirs creates the necessary directories for cpak to work.
func createCpakDirs(options *types.CpakOptions) error {
	dirs := []string{
		options.BinPath,
		options.ManifestsPath,
		options.ExportsPath,
		options.StorePath,
		options.CachePath,

		// Store subdirectories
		options.StoreLayersPath,
		options.StoreContainersPath,
		options.StoreStatesPath,
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			err = os.MkdirAll(dir, 0755)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// Audit checks the integrity of the local store and repairs it if needed.
// If the repair flag is set to true, the function will try to repair the
// store, by removing inactivated containers and missing applications.
func (c *Cpak) Audit(repair bool) (err error) {
	logger.Println("Starting cpak store audit...")
	if repair {
		logger.Println("Repair mode enabled.")
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return fmt.Errorf("audit: failed to open store: %w", err)
	}
	defer store.Close()

	allDbApps, err := store.GetApplications()
	if err != nil {
		return fmt.Errorf("audit: failed to get applications from DB: %w", err)
	}

	// --- 1. Applications in the DB vs Layers on the Filesystem ---
	logger.Println("\nChecking application layers...")
	appsToPotentiallyRemove := make(map[string]string)

	for _, app := range allDbApps {
		logger.Printf("  Auditing app: %s (Origin: %s, Version: %s)", app.Name, app.Origin, app.Version)
		if len(app.ParsedLayers) == 0 && app.Config != "" {
			logger.Printf("    [WARNING] App %s has OCI config but no layers listed in DB.", app.CpakId)
		}
		for _, layerDigest := range app.ParsedLayers {
			layerPath := c.GetInStoreDir("layers", layerDigest)
			if _, statErr := os.Stat(layerPath); os.IsNotExist(statErr) {
				reason := fmt.Sprintf("layer %s for app %s (CpakId: %s) not found at %s", layerDigest, app.Name, app.CpakId, layerPath)
				logger.Printf("    [ERROR] %s", reason)
				appsToPotentiallyRemove[app.CpakId] = reason
			}
		}
	}
	if repair && len(appsToPotentiallyRemove) > 0 {
		logger.Println("  Repairing missing layers for applications (marking for removal, manual intervention might be needed):")
		for cpakId, reason := range appsToPotentiallyRemove {
			logger.Printf("    App %s is corrupted due to missing layers (%s). Consider removing and reinstalling.", cpakId, reason)
		}
		allDbApps, _ = store.GetApplications()
	}

	// --- 2. Layers Garbage Collection ---
	logger.Println("\nChecking for orphaned layers (Garbage Collection)...")
	allReferencedLayers := make(map[string]bool)
	for _, app := range allDbApps {
		for _, layerDigest := range app.ParsedLayers {
			allReferencedLayers[layerDigest] = true
		}
	}

	layerStorePath := c.GetInStoreDir("layers")
	diskLayers, err := os.ReadDir(layerStorePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Printf("  [ERROR] Could not read layers directory %s: %v", layerStorePath, err)
		}
	} else {
		for _, diskLayerEntry := range diskLayers {
			if diskLayerEntry.IsDir() {
				layerDigestOnDisk := diskLayerEntry.Name()
				if !allReferencedLayers[layerDigestOnDisk] {
					layerFullPath := filepath.Join(layerStorePath, layerDigestOnDisk)
					logger.Printf("  Layer %s found on disk but not referenced by any application.", layerFullPath)
					if repair {
						logger.Printf("    Repair: Removing orphaned layer %s...", layerFullPath)
						if removeErr := os.RemoveAll(layerFullPath); removeErr != nil {
							logger.Printf("      [ERROR] Failed to remove orphaned layer %s: %v", layerFullPath, removeErr)
						} else {
							logger.Printf("      Orphaned layer %s removed.", layerFullPath)
						}
					}
				}
			}
		}
	}

	// --- 3. Containers in the DB vs Filesystem and Process States ---
	logger.Println("\nChecking container integrity and process states...")
	allDbContainers := []types.Container{}
	for _, app := range allDbApps {
		appContainers, _ := store.GetApplicationContainers(app)
		allDbContainers = append(allDbContainers, appContainers...)
	}

	for _, container := range allDbContainers {
		logger.Printf("  Auditing container: %s (App CpakId: %s)", container.CpakId, container.ApplicationCpakId)
		validContainer := true

		if _, statErr := os.Stat(container.StatePath); os.IsNotExist(statErr) {
			logger.Printf("    [ERROR] State path %s for container %s not found.", container.StatePath, container.CpakId)
			validContainer = false
		}
		containerRootfs := c.GetInStoreDir("containers", container.CpakId, "rootfs")
		if _, statErr := os.Stat(containerRootfs); os.IsNotExist(statErr) {
			logger.Printf("    [ERROR] RootFS path %s for container %s not found.", containerRootfs, container.CpakId)
			validContainer = false
		}

		if container.Pid != 0 {
			pidExists, _ := process.PidExists(int32(container.Pid))
			if !pidExists {
				logger.Printf("    [INFO] Main process PID %d for container %s is not running.", container.Pid, container.CpakId)
				if repair {
					logger.Printf("      Repair: Container %s main process is not running. Cleaning up associated files and DB entry.", container.CpakId)
					validContainer = false
				}
			}
		}

		if container.HostExecPid != 0 {
			pidExists, _ := process.PidExists(int32(container.HostExecPid))
			if !pidExists {
				logger.Printf("    [INFO] HostExec server PID %d for container %s is not running.", container.HostExecPid, container.CpakId)
			}
		}

		if !validContainer && repair {
			logger.Printf("    Repair: Removing invalid container DB entry %s and associated files.", container.CpakId)
			if container.HostExecPid != 0 {
				stopHostExecServer(container.HostExecPid)
			}
			_ = os.RemoveAll(container.StatePath)
			_ = os.RemoveAll(containerRootfs)
			_ = os.RemoveAll(filepath.Dir(containerRootfs))

			if removeErr := store.RemoveContainerByCpakId(container.CpakId); removeErr != nil {
				logger.Printf("      [ERROR] Failed to remove container %s from DB: %v", container.CpakId, removeErr)
			} else {
				logger.Printf("      Container %s removed from DB.", container.CpakId)
			}
		}
	}

	// --- 4. Orphaned Container/State Directories ---
	logger.Println("\nChecking for orphaned container/state directories...")
	checkOrphanedDirs := func(basePath string, description string, getDbIdsFunc func() (map[string]bool, error)) {
		diskEntries, err := os.ReadDir(basePath)
		if err != nil {
			if !os.IsNotExist(err) {
				logger.Printf("  [ERROR] Could not read %s directory %s: %v", description, basePath, err)
			}
			return
		}

		dbIds, err := getDbIdsFunc()
		if err != nil {
			logger.Printf("  [ERROR] Could not get DB IDs for %s: %v", description, err)
			return
		}

		for _, entry := range diskEntries {
			if entry.IsDir() {
				idOnDisk := entry.Name()
				if !dbIds[idOnDisk] {
					fullPath := filepath.Join(basePath, idOnDisk)
					logger.Printf("  Orphaned %s directory found: %s", description, fullPath)
					if repair {
						logger.Printf("    Repair: Removing orphaned %s directory %s...", description, fullPath)
						if removeErr := os.RemoveAll(fullPath); removeErr != nil {
							logger.Printf("      [ERROR] Failed to remove %s: %v", fullPath, removeErr)
						} else {
							logger.Printf("      %s directory %s removed.", description, fullPath)
						}
					}
				}
			}
		}
	}

	getContainerDbIds := func() (map[string]bool, error) {
		ids := make(map[string]bool)
		currentDbContainers := []types.Container{}
		for _, app := range allDbApps {
			appContainers, _ := store.GetApplicationContainers(app)
			currentDbContainers = append(currentDbContainers, appContainers...)
		}
		for _, cont := range currentDbContainers {
			ids[cont.CpakId] = true
		}
		return ids, nil
	}

	checkOrphanedDirs(c.Options.StoreContainersPath, "container rootfs", getContainerDbIds)
	checkOrphanedDirs(c.Options.StoreStatesPath, "state", getContainerDbIds)

	// --- 5. Exports (Binaries and .desktop Files) ---
	logger.Println("\nChecking application exports (binaries and .desktop files)...")
	homeDir, _ := os.UserHomeDir()
	desktopEntriesPath := filepath.Join(homeDir, ".local", "share", "applications")

	expectedExports := make(map[string]string)

	for _, app := range allDbApps {
		for _, binaryName := range app.ParsedBinaries {
			exportPath := filepath.Join(c.Options.ExportsPath, filepath.Join(strings.Split(app.Origin, "/")...), filepath.Base(binaryName))
			expectedExports[exportPath] = app.CpakId
			if _, statErr := os.Stat(exportPath); os.IsNotExist(statErr) {
				logger.Printf("  [WARNING] Exported binary %s for app %s (Origin: %s) not found.", exportPath, app.Name, app.Origin)
				if repair {
					logger.Printf("    Repair: Recreating binary export for %s...", exportPath)
					if c.exportBinary(app, binaryName) != nil {
						logger.Printf("      [ERROR] Failed to recreate binary %s.", exportPath)
					}
				}
			}
		}
		for _, desktopEntryName := range app.ParsedDesktopEntries {
			baseName := filepath.Base(desktopEntryName)
			exportPath := filepath.Join(desktopEntriesPath, baseName)
			expectedExports[exportPath] = app.CpakId
			if _, statErr := os.Stat(exportPath); os.IsNotExist(statErr) {
				logger.Printf("  [WARNING] Exported .desktop file %s for app %s (Origin: %s) not found.", exportPath, app.Name, app.Origin)
				if repair {
					logger.Printf("    Repair: Recreating .desktop entry for %s...", exportPath)
					logger.Println("      Automatic recreation of .desktop file during audit is complex. Please reinstall or re-export if needed.")
				}
			}
		}
	}

	// Check for orphaned exports
	// Binaries
	filepath.WalkDir(c.Options.ExportsPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			if _, expected := expectedExports[path]; !expected {
				logger.Printf("  Orphaned binary export found: %s", path)
				if repair {
					logger.Printf("    Repair: Removing orphaned binary export %s...", path)
					if removeErr := os.Remove(path); removeErr != nil {
						logger.Printf("      [ERROR] Failed to remove %s: %v", path, removeErr)
					}
				}
			}
		}
		return nil
	})

	// .desktop files (only those that appear to be managed by cpak)
	desktopFiles, _ := os.ReadDir(desktopEntriesPath)
	for _, df := range desktopFiles {
		if !df.IsDir() {
			fullDesktopPath := filepath.Join(desktopEntriesPath, df.Name())
			content, readErr := os.ReadFile(fullDesktopPath)
			if readErr == nil && strings.Contains(string(content), "Exec=cpak run") {
				if _, expected := expectedExports[fullDesktopPath]; !expected {
					appExistsForThisExport := false
					for _, app := range allDbApps {
						for _, deName := range app.ParsedDesktopEntries {
							if filepath.Base(deName) == df.Name() {
								appExistsForThisExport = true
								break
							}
						}
						if appExistsForThisExport {
							break
						}
					}

					if !appExistsForThisExport {
						logger.Printf("  Potentially orphaned .desktop file found: %s (managed by cpak)", fullDesktopPath)
						if repair {
							logger.Printf("    Repair: Removing orphaned .desktop file %s...", fullDesktopPath)
							if removeErr := os.Remove(fullDesktopPath); removeErr != nil {
								logger.Printf("      [ERROR] Failed to remove %s: %v", fullDesktopPath, removeErr)
							}
						}
					}
				}
			}
		}
	}

	logger.Println("\nAudit finished.")
	return nil
}
