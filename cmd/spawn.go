package cmd

/*
cpak spawn -c <container-id> -r <rootfs> -e <env> -l <layers> -s <state-dir> -d <layers-dir>
*/

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/spf13/cobra"
)

func NewSpawnCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Spawn a new namespace",
		RunE:  SpawnPackage,
	}

	cmd.Flags().String("container-id", "c", "set the container id")
	cmd.Flags().String("rootfs", "r", "set the rootfs")
	cmd.Flags().StringArrayP("env", "e", []string{}, "set environment variables")
	cmd.Flags().String("layers", "l", "set the layers")
	cmd.Flags().String("state-dir", "s", "set the state directory")
	cmd.Flags().String("image-dir", "i", "set the image directory")
	cmd.Flags().String("layers-dir", "d", "set the layers directory")
	cmd.Flags().StringArrayP("mount-overrides", "m", []string{}, "set the mount overrides")

	return cmd
}

func spawnError(prefix string, iErr error) (err error) {
	if prefix != "" {
		prefix = prefix + ": "
	}
	err = fmt.Errorf(prefix, "an error occurred while spawning the namespace: %s", iErr)
	return
}

func SpawnPackage(cmd *cobra.Command, args []string) (err error) {
	containerId, err := cmd.Flags().GetString("container-id")
	if err != nil {
		return spawnError("", err)
	}
	rootFs, err := cmd.Flags().GetString("rootfs")
	if err != nil {
		return spawnError("", err)
	}
	envVars, err := cmd.Flags().GetStringArray("env")
	if err != nil {
		return spawnError("", err)
	}
	layers, err := cmd.Flags().GetString("layers")
	if err != nil {
		return spawnError("", err)
	}
	stateDir, err := cmd.Flags().GetString("state-dir")
	if err != nil {
		return spawnError("", err)
	}
	layersDir, err := cmd.Flags().GetString("layers-dir")
	if err != nil {
		return spawnError("", err)
	}
	overrideMounts, err := cmd.Flags().GetStringArray("mount-overrides")
	if err != nil {
		return spawnError("", err)
	}

	fmt.Println("Remounting as private")
	err = syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, "")
	if err != nil {
		return spawnError("mount", err)
	}

	layersAsList := parseLayers(layers)
	for _, layer := range layersAsList {
		err = mountLayer(rootFs, layersDir, stateDir, layer)
		if err != nil {
			return err
		}
	}

	err = setupMountPoints(rootFs, overrideMounts)
	if err != nil {
		return err
	}

	err = injectConfigurationFiles(rootFs)
	if err != nil {
		return err
	}

	err = pivotRoot(rootFs)
	if err != nil {
		return err
	}

	// hostname is not set because it will raise problems with the StartUpWMClass
	// in the exported desktop file(s), resulting in a new icon for each container
	// instead of grouping them, e.g. in the GNOME shell dock
	// err = setHostname(containerId)
	// if err != nil {
	// 	return err
	// }

	envVars = setEnvironmentVariables(containerId, rootFs, envVars, stateDir, layersDir, layers)
	err = startSleepProcess(envVars)
	if err != nil {
		return err
	}

	return nil
}

func setEnvironmentVariables(containerId, rootFs string, envVars []string, stateDir, layersDir, layers string) []string {
	envVars = append(envVars, "CPAK_CONTAINER_ID="+containerId)
	envVars = append(envVars, "CPAK_ROOTFS="+rootFs)
	envVars = append(envVars, "CPAK_STATE_DIR="+stateDir)
	envVars = append(envVars, "CPAK_LAYERS_DIR="+layersDir)
	envVars = append(envVars, "CPAK_LAYERS="+layers)
	return envVars
}

func parseLayers(layers string) []string {
	layersAsList := []string{}
	if layers != "" {
		for _, layer := range strings.Split(layers, "|") {
			if layer != "" {
				layersAsList = append(layersAsList, layer)
			}
		}
	}
	return layersAsList
}

func mountLayer(rootFs, layersDir, stateDir, layer string) error {
	layerDir := filepath.Join(layersDir, layer)
	fmt.Println("Mounting:layerDir: ", layerDir)
	fmt.Println("Mounting:rootFs: ", rootFs)
	fmt.Println("Mounting:stateDir: ", stateDir)
	fmt.Println("Mounting:layer: ", layer)
	err := tools.MountOverlay(rootFs, layerDir, stateDir)
	if err != nil {
		fmt.Println("OverlayFS mount failed, trying with FUSE OverlayFS")
		err = tools.MountFuseOverlayfs(rootFs, layerDir, stateDir)
		if err != nil {
			return spawnError("mount:layer"+layer, err)
		}
	}
	return nil
}

func setupMountPoints(rootFs string, overrideMounts []string) error {
	mounts := []string{
		"/proc/", // TODO: there is a problem with spawning processes without /proc
		"/sys/",
		//"/dev",
		//"/dev/pts",
		//"/dev/shm",
		"/tmp/",
		//"/run",
		//homeDir,
	}
	mounts = append(mounts, overrideMounts...)

	for _, mount := range mounts {
		fmt.Println("(override) Mounting: ", mount)

		info, err := os.Stat(filepath.Join(rootFs, mount))
		if os.IsNotExist(err) {
			fmt.Println("does not exist", mount)
			if strings.HasSuffix(mount, "/") {
				fmt.Println("is dir, creating", mount)
				err = os.MkdirAll(filepath.Join(rootFs, mount), 0755)
				if err != nil {
					return spawnError("mkdir:"+mount, err)
				}
			} else {
				fmt.Println("is file, creating", mount)
				parentDir := filepath.Dir(mount)
				fmt.Println("parentDir", parentDir)
				err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
				if err != nil {
					return spawnError("mkdir:"+parentDir, err)
				}
				fmt.Println("creating file", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return spawnError("create:"+mount, err)
				}
				err = file.Close()
				if err != nil {
					return spawnError("close:"+mount, err)
				}
			}
		} else if err == nil {
			fmt.Println("exists", mount)
			if !info.IsDir() {
				fmt.Println("is file, creating", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return spawnError("create:"+mount, err)
				}
				err = file.Close()
				if err != nil {
					return spawnError("close:"+mount, err)
				}
			}
		}

		err = tools.MountBind(mount, filepath.Join(rootFs, mount))
		if err != nil {
			return spawnError("mount:"+mount, err)
		}
	}
	return nil
}

func injectConfigurationFiles(rootFs string) error {
	nvidiaLibs, err := cpak.GetNvidiaLibs()
	if err != nil {
		return spawnError("", err)
	}

	files := []string{
		"/etc/resolv.conf",
		"/etc/hosts",
		"/etc/passwd",
	}
	files = append(files, nvidiaLibs...)

	for _, conf := range files {
		parentDir := filepath.Dir(conf)
		err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
		if err != nil {
			return spawnError("mkdir:"+parentDir, err)
		}

		fmt.Println("Mounting: ", conf)
		err = tools.MountBind(conf, filepath.Join(rootFs, conf))
		if err != nil {
			return spawnError("mount:"+conf, err)
		}
	}
	return nil
}

func pivotRoot(rootFs string) error {
	fmt.Println("Pivoting: ", rootFs)
	pivotDir := filepath.Join(rootFs, ".pivot_root")
	err := os.MkdirAll(pivotDir, 0755)
	if err != nil {
		return spawnError("mkdir:"+pivotDir, err)
	}

	err = syscall.PivotRoot(rootFs, pivotDir)
	if err != nil {
		return spawnError("pivot_root", err)
	}

	err = os.Chdir("/")
	if err != nil {
		return spawnError("chdir", err)
	}
	return nil
}

// func setHostname(containerId string) error {
// 	fmt.Println("Setting hostname: ", containerId)
// 	err := syscall.Sethostname([]byte(fmt.Sprintf("cpak-%s", containerId[:12])))
// 	if err != nil {
// 		return spawnError("sethostname", err)
// 	}
// 	return nil
// }

func startSleepProcess(envVars []string) error {
	fmt.Println("Starting sleep process")
	envv := append(os.Environ(), envVars...)
	c := exec.Command("sleep", "infinity")
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = envv

	err := c.Start()
	if err != nil {
		return spawnError("start", err)
	}

	err = c.Process.Release()
	if err != nil {
		return spawnError("release", err)
	}

	return nil
}
