package cpak

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/linux-immutability-tools/containers-wrapper/pkg/cengine"
	ceTypes "github.com/linux-immutability-tools/containers-wrapper/pkg/types"
	"github.com/mirkobrombin/cpak/pkg/types"
)

//go:embed podman-launcher
var podmanLauncherBytes []byte

type Cpak struct {
	Ce      cengine.Ce
	Options types.CpakOptions
}

func NewCpak() (cpak Cpak, err error) {
	cpak.Options, err = getCpakOptions()
	if err != nil {
		return
	}

	cpak.Ce, err = cengine.NewCe(ceTypes.CeOptions{
		ContainerEngine: cpak.Options.ContainerEngine,
		Env:             []string{"PODMAN_STATIC_TARGET_DIR=" + cpak.Options.BinPath + "/podman"},
	})
	return
}

// getCpakOptions returns the system-wide cpak options
// it looks for them in the following order:
// 1. environment variables: CPAK_ENGINE, CPAK_INSTALLATION_PATH or CPAK_OPTS_FILE
// 2. mode: CPAK_MODE
// 3. config file:
//   - $HOME/.config/cpak/cpak.json
//   - /etc/cpak/cpak.json
//   - /usr/share/cpak/cpak.json
//
// If no options are found, it returns the default options, Cpak will use a
// builtin container engine and the default installation path will be
// $HOME/.local/share/cpak.
func getCpakOptions() (options types.CpakOptions, err error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	confPaths := []string{}

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
			return
		}
	}

	installationPath := filepath.Join(homedir, ".local", "share", "cpak")

	if os.Getenv("CPAK_INSTALLATION_PATH") != "" {
		installationPath = os.Getenv("CPAK_INSTALLATION_PATH")
	}

	options = types.CpakOptions{
		BinPath:       filepath.Join(installationPath, "bin"),
		ManifestsPath: filepath.Join(installationPath, "manifests"),
		ExportsPath:   filepath.Join(installationPath, "exports"),
		StorePath:     filepath.Join(installationPath, "store"),
		CachePath:     filepath.Join(installationPath, "cache"),
	}

	err = createCpakDirs(&options)
	if err != nil {
		return
	}

	if os.Getenv("CPAK_ENGINE") == "" {
		// options.ContainerEngine, err = setupBuiltinContainerEngine(options.BinPath)
		options.ContainerEngine = "podman"
	} else {
		options.ContainerEngine = os.Getenv("CPAK_ENGINE")
	}

	if os.Getenv("CPAK_MODE") == "" {
		options.Mode = "keep"
	} else {
		if os.Getenv("CPAK_MODE") != "keep" && os.Getenv("CPAK_MODE") != "drop" {
			err = fmt.Errorf("invalid CPAK_MODE: %s. It must be either 'keep' or 'drop'", os.Getenv("CPAK_MODE"))
			return
		}
		options.Mode = os.Getenv("CPAK_MODE")
	}

	return
}

func readCpakOptions(path string) (options types.CpakOptions, err error) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	err = json.NewDecoder(file).Decode(&options)
	return
}

func createCpakDirs(options *types.CpakOptions) error {
	dirs := []string{
		options.BinPath,
		options.ManifestsPath,
		options.ExportsPath,
		options.StorePath,
		options.CachePath,
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

func setupBuiltinContainerEngine(binPath string) (string, error) {
	podmanLauncherPath := filepath.Join(binPath, "podman-launcher")
	if _, err := os.Stat(podmanLauncherPath); err == nil {
		return podmanLauncherPath, nil
	}

	err := os.WriteFile(podmanLauncherPath, podmanLauncherBytes, 0755)
	if err != nil {
		return "", err
	}

	return podmanLauncherPath, nil
}
