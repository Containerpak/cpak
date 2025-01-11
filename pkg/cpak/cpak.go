package cpak

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/pkg/storage"
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
	options.HostSpawnBinPath = filepath.Join(options.BinPath, "host-spawn")

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
	fmt.Println("TODO: implement audit")
	return
}
