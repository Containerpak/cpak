/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/pkg/storage"
	"github.com/mirkobrombin/go-foundation/v2/core/configuration"
	configenv "github.com/mirkobrombin/go-foundation/v2/core/configuration/source/env"
	configfile "github.com/mirkobrombin/go-foundation/v2/core/configuration/source/file"
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

func getCpakOptions() (options types.CpakOptions, err error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	installationPath := os.Getenv("CPAK_INSTALLATION_PATH")
	if installationPath == "" {
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

	options.StoreLayersPath = filepath.Join(options.StorePath, "layers")
	options.StoreContainersPath = filepath.Join(options.StorePath, "containers")
	options.StoreStatesPath = filepath.Join(options.StorePath, "states")
	options.RotlesskitBinPath = filepath.Join(options.BinPath, "rootlesskit")
	options.NsenterBinPath = filepath.Join(options.BinPath, "nsenter")

	err = createCpakDirs(&options)
	if err != nil {
		return
	}

	err = tools.EnsureUnixDeps(options.BinPath, "rootlesskit")
	if err != nil {
		return
	}

	return options, nil
}

func bindDaBaDeeOptions(config *configuration.Configuration, options *storage.StorageOptions) {
	if value, ok := config.GetString("dabadee_store:root"); ok {
		options.Root = value
	} else if value, ok := config.GetString("dabadee_store_root"); ok {
		options.Root = value
	}
	if value, ok := config.GetBool("dabadee_store:withmetadata"); ok {
		options.WithMetadata = value
	} else if value, ok := config.GetBool("dabadee_store_with_metadata"); ok {
		options.WithMetadata = value
	}
	if value, ok := config.Get("dabadee_store:paths"); ok {
		options.Paths = stringSlice(value)
	} else if value, ok := config.GetString("dabadee_store_paths"); ok {
		options.Paths = stringSlice(value)
	}
}

func stringSlice(value any) []string {
	switch values := value.(type) {
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			result = append(result, fmt.Sprint(item))
		}
		return result
	case []string:
		return values
	case string:
		if values == "" {
			return nil
		}
		return strings.Split(values, ",")
	default:
		return nil
	}
}

func createCpakDirs(options *types.CpakOptions) error {
	dirs := []string{
		options.BinPath,
		options.ManifestsPath,
		options.ExportsPath,
		options.StorePath,
		options.CachePath,
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

	logger.Println("\nChecking application layers...")
	appsToPotentiallyRemove := make(map[string]string)

	for _, app := range allDbApps {
		logger.Printf("  Auditing app: %s (Origin: %s, Version: %s)", app.Name, app.Origin, app.Version)
		for _, layerDigest := range app.ParsedLayers {
			layerPath := c.GetInStoreDir("layers", layerDigest)
			if _, statErr := os.Stat(layerPath); os.IsNotExist(statErr) {
				reason := fmt.Sprintf("layer %s for app %s (CpakId: %s) not found at %s", layerDigest, app.Name, app.CpakId, layerPath)
				logger.Printf("    [ERROR] %s", reason)
				appsToPotentiallyRemove[app.CpakId] = reason
			}
		}
	}

	logger.Println("\nChecking store garbage...")
	if err := c.collectGarbage(allDbApps, repair); err != nil {
		return fmt.Errorf("audit: garbage collection failed: %w", err)
	}

	// Containers check
	logger.Println("\nChecking container integrity and process states...")
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
					stopHostExecServer(container.HostExecPid)
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
