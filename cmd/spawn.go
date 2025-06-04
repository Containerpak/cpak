/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/spf13/cobra"
)

var verbose = false

const cpakInContainerPath = "/usr/local/bin/cpak"
const hostExecShimPath = "/usr/local/bin/cpak-hostexec-shim"

func NewSpawnCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "spawn",
		Short:  "Spawn a new namespace",
		RunE:   SpawnPackage,
		Hidden: true,
	}

	cmd.Flags().BoolP("verbose", "v", false, "enable verbose output")
	cmd.Flags().Int("user-uid", 0, "set the user uid")
	cmd.Flags().String("app-id", "a", "set the app id")
	cmd.Flags().String("container-id", "c", "set the container id")
	cmd.Flags().String("rootfs", "r", "set the rootfs")
	cmd.Flags().StringArrayP("env", "e", []string{}, "set environment variables")
	cmd.Flags().String("layers", "l", "set the layers")
	cmd.Flags().String("state-dir", "s", "set the state directory")
	cmd.Flags().String("image-dir", "i", "set the image directory")
	cmd.Flags().String("layers-dir", "d", "set the layers directory")
	cmd.Flags().StringArrayP("mount-overrides", "m", []string{}, "set the mount overrides")
	cmd.Flags().StringArrayP("mount-shims", "M", []string{}, "set the mount shims")
	cmd.Flags().StringArrayP("extra-links", "x", []string{}, "set the extra links")

	return cmd
}

func spawnError(prefix string, iErr error) (err error) {
	if prefix != "" {
		prefix = prefix + ": "
	}
	err = fmt.Errorf(prefix, "an error occurred while spawning the namespace: %s", iErr)
	return
}

func spawnVerbose(args ...any) {
	if verbose {
		msg := []any{"[verbose]: "}
		msg = append(msg, args...)
		logger.Println(msg...)
	}
}

func SpawnPackage(cmd *cobra.Command, args []string) (err error) {
	verbose, _ = cmd.Flags().GetBool("verbose")

	logger.Println("Spawning a new cpak namespace...")

	userUid, err := cmd.Flags().GetInt("user-uid")
	if err != nil {
		return spawnError("user-uid flag", err)
	}
	appId, err := cmd.Flags().GetString("app-id")
	if err != nil {
		return spawnError("app-id flag", err)
	}
	containerId, err := cmd.Flags().GetString("container-id")
	if err != nil {
		return spawnError("container-id flag", err)
	}
	rootFs, err := cmd.Flags().GetString("rootfs") // This is the HOST path to the rootfs mount point
	if err != nil {
		return spawnError("rootfs flag", err)
	}
	envVars, err := cmd.Flags().GetStringArray("env")
	if err != nil {
		return spawnError("env flag", err)
	}
	layers, err := cmd.Flags().GetString("layers")
	if err != nil {
		return spawnError("layers flag", err)
	}
	stateDir, err := cmd.Flags().GetString("state-dir")
	if err != nil {
		return spawnError("state-dir flag", err)
	}
	layersDir, err := cmd.Flags().GetString("layers-dir")
	if err != nil {
		return spawnError("layers-dir flag", err)
	}
	overrideMounts, err := cmd.Flags().GetStringArray("mount-overrides")
	if err != nil {
		return spawnError("mount-overrides flag", err)
	}
	overrideMountShims, err := cmd.Flags().GetStringArray("mount-shims")
	if err != nil {
		return spawnError("mount-shims flag", err)
	}
	extraLinks, err := cmd.Flags().GetStringArray("extra-links")
	if err != nil {
		return spawnError("extra-links flag", err)
	}

	var hostExecSocketPath string
	var allowedHostCmdsStr string
	finalEnvVarsForContainer := []string{}
	for _, envVar := range envVars {
		if strings.HasPrefix(envVar, "CPAK_HOSTEXEC_SOCKET=") {
			logger.Println("Found hostexec socket path in env:", envVar)
			hostExecSocketPath = strings.TrimPrefix(envVar, "CPAK_HOSTEXEC_SOCKET=")
			finalEnvVarsForContainer = append(finalEnvVarsForContainer, envVar)
		} else if strings.HasPrefix(envVar, "CPAK_ALLOWED_HOST_CMDS=") {
			allowedHostCmdsStr = strings.TrimPrefix(envVar, "CPAK_ALLOWED_HOST_CMDS=")
		} else {
			finalEnvVarsForContainer = append(finalEnvVarsForContainer, envVar)
		}
	}
	allowedHostCmds := []string{}
	if allowedHostCmdsStr != "" {
		allowedHostCmds = strings.Split(allowedHostCmdsStr, ":")
	}

	spawnVerbose("Remounting as private")
	err = syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, "")
	if err != nil {
		return spawnError("mount", err)
	}

	layersAsList := parseLayers(layers)
	err = mountLayers(rootFs, layersDir, stateDir, layersAsList)
	if err != nil {
		return err
	}

	err = setupMountPoints(userUid, rootFs, overrideMounts)
	if err != nil {
		return err
	}

	err = injectConfigurationFiles(rootFs)
	if err != nil {
		return err
	}

	err = setupExtraLinks(rootFs, extraLinks)
	if err != nil {
		return err
	}

	// Append shims obtained by overrides, to the allowed commands
	if len(overrideMountShims) > 0 {
		logger.Println("Found mount shims in overrides:", overrideMountShims)
		allowedHostCmds = append(allowedHostCmds, overrideMountShims...)
	}

	if len(allowedHostCmds) > 0 && hostExecSocketPath != "" {
		spawnVerbose("Creating hostexec shim and symlinks")
		err = createHostExecShimAndLinks(rootFs, allowedHostCmds)
		if err != nil {
			return err
		}
		spawnVerbose("Hostexec shim script and symlinks created.")
	} else {
		spawnVerbose("Skipping hostexec shim creation (no allowed commands or socket path).")
	}

	err = pivotRoot(rootFs)
	if err != nil {
		return err
	}

	err = createCpakFile(appId, rootFs)
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

	_envVars := setEnvironmentVariables(containerId, rootFs, finalEnvVarsForContainer, stateDir, layersDir, layers)
	err = startSleepProcess(args, _envVars)
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

// the .cpak file is used to check if we are inside a cpak container
func createCpakFile(appId string, rootFs string) error {
	spawnVerbose("Creating cpak file")

	err := os.MkdirAll(filepath.Join(rootFs, "/tmp"), 0755)
	if err != nil {
		return spawnError("mkdir:/tmp", err)
	}
	file, err := os.Create(filepath.Join(rootFs, "/tmp", ".cpak"))
	if err != nil {
		return spawnError("create", err)
	}
	defer file.Close()

	_, err = file.WriteString(appId)
	if err != nil {
		return spawnError("write", err)
	}

	return nil
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

func mountLayers(rootFs, layersDir string, stateDir string, layersList []string) error {
	layersDirs := ""

	for _, layer := range layersList {
		layerDir := filepath.Join(layersDir, layer)
		layersDirs = layersDirs + ":" + layerDir
	}

	layersDirs = layersDirs[1:]

	err := tools.MountOverlay(rootFs, layersDirs, filepath.Join(stateDir, "up"), filepath.Join(stateDir, "work"))
	if err != nil {
		return spawnError("mount:layers "+layersDirs, err)
	}
	return nil
}

func setupMountPoints(userUid int, rootFs string, overrideMounts []string) error {
	// /tmp is mounted as a new one
	spawnVerbose("Mounting: /tmp")
	err := tools.MountTmpfs(filepath.Join(rootFs, "/tmp"))
	if err != nil {
		return spawnError("mount:/tmp", err)
	}

	mounts := []string{
		"/proc/", // TODO: there is a problem with spawning processes without /proc
		"/sys/",
		//"/dev",
		//"/dev/pts",
		//"/dev/shm",
		//"/tmp/",
		//"/run",
		//homeDir,
	}
	mounts = append(mounts, overrideMounts...)

	for _, mount := range mounts {
		spawnVerbose("(override) Mounting: ", mount)

		// we skip mounts that do not exist on the host, this should be
		// safe because those mounts come from the overrides list which
		// are expected to be dbus and other sockets, this will just disable
		// the feature of the container to use those sockets
		_, err := os.Stat(mount)
		if os.IsNotExist(err) {
			spawnVerbose(mount, "does not exist, that's probably unsupported by the host, ignoring")
			continue
		}

		_, err = os.Stat(filepath.Join(rootFs, mount))
		if os.IsNotExist(err) {
			spawnVerbose("does not exist", mount)
			if strings.HasSuffix(mount, "/") {
				spawnVerbose("is dir, creating", mount)
				err = os.MkdirAll(filepath.Join(rootFs, mount), 0755)
				if err != nil {
					return spawnError("mkdir:"+mount, err)
				}
			} else {
				spawnVerbose("is file, creating", mount)
				parentDir := filepath.Dir(mount)
				spawnVerbose("parentDir", parentDir)
				err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
				if err != nil {
					return spawnError("mkdir:"+parentDir, err)
				}
				spawnVerbose("creating file", mount)
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
			spawnVerbose("exists", mount)
			if !strings.HasSuffix(mount, "/") {
				spawnVerbose("is file, creating", mount)
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

	// the cpak socket is mounted as last because it is created by another
	// process and we need to wait for it to be available. However, it should
	// be available at this point
	cpakSockPath := "/tmp/cpak.sock"
	spawnVerbose("Waiting for: ", cpakSockPath, "to be available...")
	for {
		_, err := os.Stat(cpakSockPath)
		if err == nil {
			spawnVerbose("Mounting: ", cpakSockPath)
			err = tools.MountBind(cpakSockPath, filepath.Join(rootFs, cpakSockPath))
			if err != nil {
				return spawnError("mount:"+cpakSockPath, err)
			}
			break
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

	for _, conf := range files {
		parentDir := filepath.Dir(conf)
		err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
		if err != nil {
			return spawnError("mkdir:"+parentDir, err)
		}

		spawnVerbose("Mounting: ", conf)
		err = tools.MountBind(conf, filepath.Join(rootFs, conf))
		if err != nil {
			return spawnError("mount:"+conf, err)
		}
	}

	for _, lib := range nvidiaLibs {
		spawnVerbose("Mounting: ", lib)
		// TODO: errors are ignored since also temp directories are returned
		//	   so they could not exist at the time of the mount
		tools.MountBind(lib, filepath.Join(rootFs, lib))
	}

	// host root is mounted in /run/host for debugging purposes
	err = tools.MountBind("/", filepath.Join(rootFs, "/run/host"))
	if err != nil {
		return spawnError("mount:/", err)
	}

	return nil
}

func setupExtraLinks(rootFs string, extraLinks []string) error {
	for _, link := range extraLinks {
		linkParts := strings.Split(link, ":")
		if len(linkParts) != 2 {
			return spawnError("invalid link format", nil)
		}

		spawnVerbose("Linking: ", linkParts[0], linkParts[1])
		err := tools.MountBind(linkParts[0], filepath.Join(rootFs, linkParts[1]))
		if err != nil {
			return spawnError("mount:"+linkParts[0]+":"+linkParts[1], err)
		}
	}
	return nil
}

func pivotRoot(rootFs string) error {
	spawnVerbose("Pivoting: ", rootFs)
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
// 	spawnVerbose("Setting hostname: ", containerId)
// 	err := syscall.Sethostname([]byte(fmt.Sprintf("cpak-%s", containerId[:12])))
// 	if err != nil {
// 		return spawnError("sethostname", err)
// 	}
// 	return nil
// }

func startSleepProcess(cmdArgs []string, envVars []string) error {
	spawnVerbose("Reconfiguring dynamic linker run-time bindings")
	l := exec.Command("ldconfig")
	err := l.Run()
	if err != nil {
		return spawnError("ldconfig", err)
	}

	spawnVerbose("Starting sleep process")
	args := []string{}
	if len(cmdArgs) > 0 {
		args = append(args, cmdArgs...)
	} else {
		args = append(args, "/bin/sleep")
		args = append(args, "infinity")
	}

	envv := append(os.Environ(), envVars...)
	c := exec.Command(args[0], args[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = envv

	for _, env := range envv {
		if strings.HasPrefix(env, "CPAK_") {
			spawnVerbose("CPAK env var found:", env)
		}
	}

	err = c.Start()
	if err != nil {
		return spawnError("start", err)
	}

	err = c.Process.Release()
	if err != nil {
		return spawnError("release", err)
	}

	return nil
}

// createHostExecShimAndLinks creates the shim script and symlinks for allowed commands.
func createHostExecShimAndLinks(rootFs string, allowedCmds []string) error {
	shimFilePath := filepath.Join(rootFs, strings.TrimPrefix(hostExecShimPath, "/"))
	shimDir := filepath.Dir(shimFilePath)

	spawnVerbose("Creating hostexec shim directory:", shimDir)
	if err := os.MkdirAll(shimDir, 0755); err != nil && !os.IsExist(err) {
		return spawnError("create shim dir "+shimDir, err)
	}

	// Render the shim script content
	content, err := cpak.RenderShim(cpakInContainerPath)
	if err != nil {
		return spawnError("render shim template", err)
	}
	if err := os.WriteFile(shimFilePath, content, 0755); err != nil {
		return spawnError("write shim file "+shimFilePath, err)
	}

	// Create symlinks for allowed commands
	linkTargetDir := filepath.Join(rootFs, "/usr/bin")
	spawnVerbose("Creating symlink directory:", linkTargetDir)
	if err := os.MkdirAll(linkTargetDir, 0755); err != nil && !os.IsExist(err) {
		return spawnError("create link target dir "+linkTargetDir, err)
	}

	for _, cmdName := range allowedCmds {
		if cmdName == "" {
			continue
		}
		linkPath := filepath.Join(linkTargetDir, cmdName)
		// Calculate relative path from link location to shim script
		relShimPath, err := filepath.Rel(linkTargetDir, shimFilePath)
		if err != nil {
			// Should not happen if paths are constructed correctly
			return spawnError(fmt.Sprintf("calculate relative path for shim from %s", linkTargetDir), err)
		}

		spawnVerbose("Creating symlink:", linkPath, "->", relShimPath)
		// Remove existing file/link if present before creating new one
		_ = os.Remove(linkPath)
		err = os.Symlink(relShimPath, linkPath)
		if err != nil {
			return spawnError(fmt.Sprintf("create symlink %s -> %s", linkPath, relShimPath), err)
		}
	}

	return nil
}
