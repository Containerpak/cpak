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

	// as a convenience, we set the environment variables for the container
	// in the current process, so that we can use them to resolve the
	// process id of the container
	envVars = append(envVars, "CPAK_CONTAINER_ID="+containerId)
	envVars = append(envVars, "CPAK_ROOTFS="+rootFs)
	envVars = append(envVars, "CPAK_STATE_DIR="+stateDir)
	envVars = append(envVars, "CPAK_LAYERS_DIR="+layersDir)
	envVars = append(envVars, "CPAK_LAYERS="+layers)

	fmt.Println("Rootfs:", rootFs)
	fmt.Println("Env:", envVars)
	fmt.Println("Layers:", layers)
	fmt.Println("State dir:", stateDir)
	fmt.Println("Layers dir:", layersDir)

	// mount layers
	layersAsList := []string{}
	if layers != "" {
		for _, layer := range strings.Split(layers, ":") {
			if layer != "" {
				layersAsList = append(layersAsList, layer)
			}
		}
	}

	for _, layer := range layersAsList {
		layerDir := filepath.Join(layersDir, layer)
		err = syscall.Mount(
			"overlay", rootFs, "overlay", 0,
			fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", rootFs, layerDir, stateDir),
		)
		if err != nil {
			return spawnError("mount:layer"+layer, err)
		}
	}

	// setup mount points
	homeDir := os.Getenv("HOME")
	homeDir, err = filepath.EvalSymlinks(homeDir)
	if err != nil {
		return spawnError("eval", err)
	}
	mounts := []string{
		"/proc",
		"/sys",
		"/dev",
		"/dev/pts",
		"/dev/shm",
		"/tmp",
		"/run",
		homeDir,
	}
	for _, mount := range mounts {
		err = os.MkdirAll(filepath.Join(rootFs, mount), 0755)
		if err != nil {
			return spawnError("mkdir:"+mount, err)
		}

		flags := syscall.MS_BIND | syscall.MS_REC | syscall.MS_PRIVATE
		if mount == "/sys" || mount == "/dev" || mount == homeDir {
			flags |= syscall.MS_REC
		}
		err = tools.Mount(mount, filepath.Join(rootFs, mount), uintptr(flags))
		if err != nil {
			return spawnError("mount:"+mount, err)
		}
	}

	// inject some configuration files, e.g. for networking
	confs := []string{
		"/etc/resolv.conf",
		"/etc/hosts",
	}
	for _, conf := range confs {
		parentDir := filepath.Dir(conf)
		err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
		if err != nil {
			return spawnError("mkdir:"+parentDir, err)
		}

		fmt.Println("Mounting", conf)
		err = tools.MountBind(conf, filepath.Join(rootFs, conf))
		if err != nil {
			return spawnError("mount:"+conf, err)
		}
	}

	// pivot root
	pivotDir := filepath.Join(rootFs, ".pivot_root")
	err = os.MkdirAll(pivotDir, 0755)
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

	envv := append(os.Environ(), envVars...)
	c := exec.Command("sleep", "infinity")
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = envv
	err = c.Start()
	if err != nil {
		return spawnError("start", err)
	}

	return
}
