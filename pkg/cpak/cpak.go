/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	storage "github.com/containerpak/storage/pkg/driver"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
	"github.com/mirkobrombin/go-foundation/v2/core/configuration"
	configenv "github.com/mirkobrombin/go-foundation/v2/core/configuration/source/env"
	configfile "github.com/mirkobrombin/go-foundation/v2/core/configuration/source/file"
	"github.com/shirou/gopsutil/process"
)

type Cpak struct {
	Options            Options
	Ctx                context.Context
	servicePID         int
	serviceSocketOwned bool
	brokerSocketOwned  bool
	storageMigration   StorageMigrationHandler
	storagePreparation StoragePreparationHandler
	storageDriver      storage.Handler
	desktopLaunch      bool
	fileSpan           *desktopFileSpan
}

// SetDesktopLaunch enables file grants for exported desktop entries.
func (c *Cpak) SetDesktopLaunch(enabled bool) {
	c.desktopLaunch = enabled
}

// SetDesktopFileSpan records how many arguments the publisher wrote on each side
// of the file placeholder, which is what decides which arguments of a menu
// launch are files the user chose. It is set from cpak's own flag in the
// exported entry, never from anything the publisher wrote.
func (c *Cpak) SetDesktopFileSpan(value string) error {
	if value == "" {
		return nil
	}
	span, err := parseDesktopFileSpan(value)
	if err != nil {
		return err
	}
	c.fileSpan = &span
	return nil
}

func (c *Cpak) desktopFileSpan() (desktopFileSpan, bool) {
	if c.fileSpan == nil {
		return desktopFileSpan{}, false
	}
	return *c.fileSpan, true
}

// NewCpak creates a new cpak instance.
func NewCpak() (cpak Cpak, err error) {
	cpak.Options, err = getCpakOptions()
	if err != nil {
		return
	}

	cpak.Ctx = context.Background()
	if migrationErr := cpak.migrateDesktopLaunchers(); migrationErr != nil {
		logger.Printf("Warning: could not update desktop launchers: %v", migrationErr)
	}
	return
}

// cpakStateDirectories names the directories this installation keeps its own
// state in. Every one of them comes from the options cpak resolved rather than
// from the default layout under the home, because CPAK_INSTALLATION_PATH and
// the per-path variables beside it move them independently, and cpak itself
// moves the whole tree to install a local package.
func (c *Cpak) cpakStateDirectories() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return types.CpakStateDirectories(
		home,
		c.Options.StorePath,
		c.Options.ExportsPath,
		c.Options.BinPath,
		c.Options.ManifestsPath,
		c.Options.CachePath,
		// The deduplication store is read through the same resolver everything
		// that opens it uses. Options.DaBaDeeStoreOptions.Root is empty in the
		// cases that matter -- a configuration that never named it, a caller
		// that built Options itself -- and the file content of every installed
		// package lives under the root that resolver falls back to, so asking
		// the field would leave exactly those installations unmasked.
		c.daBaDeeStoreOptions().Root,
		filepath.Dir(c.Options.RegistryAuthPath),
	)
}

func getCpakOptions() (options Options, err error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	installationPath := os.Getenv("CPAK_INSTALLATION_PATH")
	if installationPath == "" {
		installationPath = filepath.Join(homedir, ".local", "share", "cpak")
	}

	options = Options{
		BinPath:       filepath.Join(installationPath, "bin"),
		ManifestsPath: filepath.Join(installationPath, "manifests"),
		ExportsPath:   filepath.Join(installationPath, "exports"),
		StorePath:     filepath.Join(installationPath, "store"),
		DaBaDeeStoreOptions: store.Options{
			Root:             filepath.Join(installationPath, "dabadee"),
			PreserveMetadata: true,
		},
		CachePath:        filepath.Join(installationPath, "cache"),
		RegistryAuthPath: filepath.Join(homedir, ".config", "cpak", "registry-auth.json"),
		StorageDriver:    "fvs",
	}

	// What cpak laid out itself, kept before anything is bound: afterwards the
	// two are one struct and nothing can tell a path cpak chose from a path
	// somebody handed it.
	layout := options

	var confPaths []string
	if os.Getenv("CPAK_OPTS_FILE") != "" {
		confPaths = append(confPaths, os.Getenv("CPAK_OPTS_FILE"))
	} else {
		confPaths = append(confPaths, filepath.Join(homedir, ".config", "cpak", "cpak.json"))
		confPaths = append(confPaths, filepath.Join("/", "etc", "cpak", "cpak.json"))
		confPaths = append(confPaths, filepath.Join("/", "usr", "share", "cpak", "cpak.json"))
	}

	builder := configuration.NewBuilder()
	for _, confPath := range confPaths {
		if _, statErr := os.Stat(confPath); statErr == nil {
			builder.Add(configfile.New(confPath))
			break
		} else if !os.IsNotExist(statErr) {
			return options, statErr
		}
	}
	builder.Add(configenv.New("CPAK_"))

	config, err := builder.Build(context.Background())
	if err != nil {
		return options, err
	}
	if err = config.Bind(&options); err != nil {
		return options, err
	}
	bindDaBaDeeOptions(config, &options.DaBaDeeStoreOptions)
	options.OperatorNamedPaths = operatorNamedPaths(layout, options)

	options.StoreLayersPath = filepath.Join(options.StorePath, "fvs", "layers")
	options.StoreContainersPath = filepath.Join(options.StorePath, "containers")
	options.StoreStatesPath = filepath.Join(options.StorePath, "states")
	createCpakDirs(&options)
	cleanupLegacyRuntimeTools(options.BinPath)

	return options, nil
}

// operatorNamedPaths answers which of the directories cpak keeps private were
// moved by the configuration.
//
// It compares what was bound against what cpak had laid out, field by field,
// because that is the only moment the difference exists. The deduplication
// store is asked of its own field rather than of the path it resolves to: with
// that field left alone the root follows the store cpak was given, and a
// directory cpak places inside somebody's tree is still cpak's to keep
// private.
func operatorNamedPaths(layout, options Options) map[string]bool {
	named := map[string]bool{}
	for _, path := range [][3]string{
		{layout.BinPath, options.BinPath, options.BinPath},
		{layout.ManifestsPath, options.ManifestsPath, options.ManifestsPath},
		{layout.ExportsPath, options.ExportsPath, options.ExportsPath},
		{layout.StorePath, options.StorePath, options.StorePath},
		{layout.CachePath, options.CachePath, options.CachePath},
		{layout.DaBaDeeStoreOptions.Root, options.DaBaDeeStoreOptions.Root, daBaDeeRoot(&options)},
	} {
		if path[0] != path[1] {
			named[path[2]] = true
		}
	}
	return named
}

func cleanupLegacyRuntimeTools(binPath string) {
	for _, name := range []string{"nsenter", "rootlessctl", "rootlesskit", "rootlesskit-docker-proxy"} {
		_ = os.Remove(filepath.Join(binPath, name))
	}
}

func bindDaBaDeeOptions(config *configuration.Configuration, options *store.Options) {
	if value, ok := config.GetString("dabadee_store:root"); ok {
		options.Root = value
	} else if value, ok := config.GetString("dabadee_store_root"); ok {
		options.Root = value
	}
	if value, ok := config.GetBool("dabadee_store:withmetadata"); ok {
		options.PreserveMetadata = value
	} else if value, ok := config.GetBool("dabadee_store_with_metadata"); ok {
		options.PreserveMetadata = value
	}
	if value, ok := config.GetBool("dabadee_store:preservemetadata"); ok {
		options.PreserveMetadata = value
	} else if value, ok := config.GetBool("dabadee_store_preserve_metadata"); ok {
		options.PreserveMetadata = value
	}
}

func (c *Cpak) daBaDeeStoreOptions() store.Options {
	options := c.Options.DaBaDeeStoreOptions
	options.Root = daBaDeeRoot(&c.Options)
	return options
}

// daBaDeeRoot answers where the deduplication store lives.
//
// It is not part of the store tree, but it holds a hardlink to every file of
// every layer, so whatever cpak does to the store it owes to this too.
func daBaDeeRoot(options *Options) string {
	if options.DaBaDeeStoreOptions.Root != "" {
		return options.DaBaDeeStoreOptions.Root
	}
	return filepath.Join(filepath.Dir(options.StorePath), "dabadee")
}

// createCpakDirs makes the directories of an installation and keeps them
// private to the user who owns it.
//
// It reports what it could not secure instead of refusing to go on. This runs
// inside getCpakOptions, which every command goes through, so a refusal here
// is not one command failing, it is cpak not starting: a single sudo run
// leaves one root-owned directory behind and used to take out cpak list and
// cpak audit --repair with everything else, and the repair is the command the
// user needed. Carrying on grants nothing, because every place that relies on
// one of these directories secures it again at the moment it uses it
// (GetInStoreDirMkdir, GetInCacheDirMkdir, GetInManifestsDirMkdir, NewStore),
// so an install or a launch on a tree this user does not own still refuses,
// and refuses while naming the operation that wanted it.
func createCpakDirs(options *Options) {
	// states/<id>/up is the overlay upper directory, so this tree holds every
	// byte an application writes, and the deduplication store beside it holds
	// a hardlink to every file of every layer. Nobody else on the machine
	// reads either. The dabadee library creates its own subdirectories at
	// 0755, which is why its root is what gets named here.
	roots := []string{
		options.BinPath,
		options.ManifestsPath,
		options.ExportsPath,
		options.StorePath,
		options.CachePath,
		daBaDeeRoot(options),
	}
	for _, root := range roots {
		if err := options.keepPrivate(root); err != nil {
			reportUnsecuredDirectory(err)
		}
	}

	// Under the store the whole spine is walked, not only the leaf: an
	// installation made by an older cpak has fvs/ at 0755 between two
	// directories this list names.
	for _, dir := range []string{
		options.StoreLayersPath,
		filepath.Join(options.StorePath, "fvs", "blocks"),
		options.StoreContainersPath,
		options.StoreStatesPath,
	} {
		if err := securePrivateDirectoryUnder(options.StorePath, dir); err != nil {
			reportUnsecuredDirectory(err)
		}
	}
}

// reportUnsecuredDirectory says what could not be made private and what it
// costs, once per directory, on the way past.
func reportUnsecuredDirectory(err error) {
	logger.Printf("Warning: %v; the operations that need it will refuse until that is fixed", err)
}

// auditPrivateTree reports every directory of the installation the rest of the
// machine can read, and tightens it when the audit is repairing.
//
// createCpakDirs and the store helpers secure what cpak creates from now on.
// An installation made before that keeps 0755 on every directory they do not
// walk past, and the deduplication store is created by its library, so this is
// the pass that converges a tree cpak has already been using.
func (c *Cpak) auditPrivateTree(repair bool) error {
	roots := []string{
		c.Options.StorePath,
		c.Options.CachePath,
		c.Options.ManifestsPath,
		c.Options.ExportsPath,
		c.Options.BinPath,
		daBaDeeRoot(&c.Options),
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			if path != root && c.contentTree(root, path) {
				return fs.SkipDir
			}
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			mode := info.Mode().Perm()
			if mode&0077 == 0 {
				return nil
			}
			logger.Printf("  %s is readable beyond its owner: %04o", path, mode)
			if !repair {
				return nil
			}
			logger.Printf("    Repair: restricting %s to its owner.", path)
			return os.Chmod(path, mode&^0077)
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// contentTree answers whether a directory holds content whose modes are not
// cpak's to decide.
//
// An unpacked layer carries the modes of the image, a container rootfs and an
// overlay upper directory carry the modes the application wrote, and all three
// are what the application sees once they are mounted: tightening them would
// change the filesystem inside the container, not the one around it. The
// deduplication store is the same content by hardlink. Each of them sits under
// a root this pass does make private, which is what keeps them out of reach.
func (c *Cpak) contentTree(root, path string) bool {
	if root == daBaDeeRoot(&c.Options) {
		return true
	}
	if root != c.Options.StorePath {
		return false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	parts := strings.Split(relative, string(filepath.Separator))
	switch {
	case len(parts) >= 4 && parts[0] == "fvs" && parts[1] == "layers":
		return true
	case len(parts) >= 2 && parts[0] == "layers":
		return true
	case len(parts) >= 3 && parts[0] == "containers" && parts[2] == "rootfs":
		return true
	case len(parts) >= 3 && parts[0] == "states" && (parts[2] == "up" || parts[2] == "work"):
		return true
	}
	return false
}

func (c *Cpak) Audit(repair bool) (err error) {
	logger.Println("Starting cpak store audit...")
	if repair {
		logger.Println("Repair mode enabled.")
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return fmt.Errorf("audit: failed to open store: %w", err)
	}

	allDbApps, err := store.GetApplications()
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("audit: failed to get applications from DB: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("audit: failed to close store: %w", err)
	}

	logger.Println("\nChecking application layers...")
	appsToPotentiallyRemove := make(map[string]string)

	for _, app := range allDbApps {
		logger.Printf("  Auditing app: %s (Origin: %s, Version: %s)", app.Name, app.Origin, app.Version)
		for _, layerDigest := range app.ParsedLayers {
			available, layerErr := c.storedLayerAvailable(layerDigest)
			if layerErr != nil || !available {
				reason := fmt.Sprintf("layer %s for app %s (CpakId: %s) is unavailable", layerDigest, app.Name, app.CpakId)
				logger.Printf("    [ERROR] %s", reason)
				appsToPotentiallyRemove[app.CpakId] = reason
			}
		}
	}

	logger.Println("\nChecking store garbage...")
	if err := c.collectGarbage(allDbApps, repair); err != nil {
		return fmt.Errorf("audit: garbage collection failed: %w", err)
	}

	logger.Println("\nChecking store permissions...")
	if err := c.auditPrivateTree(repair); err != nil {
		return fmt.Errorf("audit: store permissions failed: %w", err)
	}

	// Containers check
	logger.Println("\nChecking container integrity and process states...")
	store, err = NewStore(c.Options.StorePath)
	if err != nil {
		return fmt.Errorf("audit: failed to reopen store: %w", err)
	}
	defer store.Close()
	for _, app := range allDbApps {
		appContainers, _ := store.GetApplicationContainers(app)
		for _, container := range appContainers {
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
				if !pidExists && repair {
					logger.Printf("      Repair: Container %s main process is not running. Cleaning up.", container.CpakId)
					validContainer = false
				}
			}

			if !validContainer && repair {
				if container.HostExecPid != 0 {
					stopLegacyHostExecServer(container.HostExecPid)
				}
				os.RemoveAll(container.StatePath)
				os.RemoveAll(filepath.Dir(containerRootfs))
				store.RemoveContainerByCpakId(container.CpakId)
			}
		}
	}

	logger.Println("\nAudit finished.")
	return nil
}
