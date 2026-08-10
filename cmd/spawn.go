/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

const cpakInContainerPath = "/usr/local/bin/cpak"
const hostExecShimPath = "/usr/local/bin/cpak-hostexec-shim"

type SpawnCmd struct {
	Verbose        bool     `cli:"verbose,v" help:"enable verbose output"`
	UserUid        int      `cli:"user-uid" help:"set the user uid"`
	AppId          string   `cli:"app-id" help:"set the app id"`
	ContainerId    string   `cli:"container-id" help:"set the container id"`
	Rootfs         string   `cli:"rootfs" help:"set the rootfs"`
	Env            []string `cli:"env,e" help:"set environment variables"`
	Layers         string   `cli:"layers" help:"set the layers"`
	StateDir       string   `cli:"state-dir" help:"set the state directory"`
	ImageDir       string   `cli:"image-dir" help:"set the image directory"`
	LayersDir      string   `cli:"layers-dir" help:"set the layers directory"`
	MountOverrides []string `cli:"mount-overrides,m" help:"set the mount overrides"`
	MountShims     []string `cli:"mount-shims,M" help:"set the mount shims"`
	ExtraLinks     []string `cli:"extra-links,x" help:"set the extra links"`
	BuildLayer     bool     `cli:"build-layer" help:"build a managed layer and exit"`
	RuntimePackage []string `cli:"runtime-package" help:"install a package in the managed layer"`
	ExtraArgs      []string `arg:"extra" help:"Extra arguments"`

	cli.Base
}

func (c *SpawnCmd) spawnVerbose(args ...any) {
	if c.Verbose {
		msg := fmt.Sprint(args...)
		c.Logger.Info("[verbose]: %s", msg)
	}
}

func (c *SpawnCmd) Run() error {
	c.Logger.Info("Spawning a new cpak namespace...")

	var hostExecSocketPath string
	var allowedHostCmdsStr string
	finalEnvVarsForContainer := []string{}
	for _, envVar := range c.Env {
		if strings.HasPrefix(envVar, "CPAK_HOSTEXEC_SOCKET=") {
			c.Logger.Info("Found hostexec socket path in env: %s", envVar)
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

	c.spawnVerbose("Remounting as private")
	err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, "")
	if err != nil {
		return fmt.Errorf("mount: an error occurred while spawning the namespace: %s", err)
	}

	layersAsList := parseLayers(c.Layers)
	err = mountLayers(c.Rootfs, c.LayersDir, c.StateDir, layersAsList)
	if err != nil {
		return err
	}
	if c.BuildLayer {
		if err = c.setupBuildMountPoints(c.Rootfs); err != nil {
			return err
		}
		if err = c.setupExtraLinks(c.Rootfs, c.ExtraLinks); err != nil {
			return err
		}
		if err = c.pivotRoot(c.Rootfs); err != nil {
			return err
		}
		return c.installRuntimePackages(c.RuntimePackage)
	}

	err = c.setupMountPoints(c.UserUid, c.Rootfs, c.MountOverrides)
	if err != nil {
		return err
	}

	err = c.injectConfigurationFiles(c.Rootfs)
	if err != nil {
		return err
	}

	err = c.setupExtraLinks(c.Rootfs, c.ExtraLinks)
	if err != nil {
		return err
	}

	// Append shims obtained by overrides, to the allowed commands
	if len(c.MountShims) > 0 {
		c.Logger.Info("Found mount shims in overrides: %v", c.MountShims)
		allowedHostCmds = append(allowedHostCmds, c.MountShims...)
	}

	if len(allowedHostCmds) > 0 && hostExecSocketPath != "" {
		c.spawnVerbose("Creating hostexec shim and symlinks")
		err = c.createHostExecShimAndLinks(c.Rootfs, allowedHostCmds)
		if err != nil {
			return err
		}
		c.spawnVerbose("Hostexec shim script and symlinks created.")
	} else {
		c.spawnVerbose("Skipping hostexec shim creation (no allowed commands or socket path).")
	}

	err = c.pivotRoot(c.Rootfs)
	if err != nil {
		return err
	}

	err = c.createCpakFile(c.AppId, c.Rootfs)
	if err != nil {
		return err
	}

	_envVars := setEnvironmentVariables(c.ContainerId, c.Rootfs, finalEnvVarsForContainer, c.StateDir, c.LayersDir, c.Layers)
	err = c.startSleepProcess(c.ExtraArgs, _envVars)
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

func (c *SpawnCmd) createCpakFile(appId string, rootFs string) error {
	c.spawnVerbose("Creating cpak file")

	err := os.MkdirAll(filepath.Join(rootFs, "/tmp"), 0755)
	if err != nil {
		return fmt.Errorf("mkdir:/tmp: an error occurred while spawning the namespace: %s", err)
	}
	file, err := os.Create(filepath.Join(rootFs, "/tmp", ".cpak"))
	if err != nil {
		return fmt.Errorf("create: an error occurred while spawning the namespace: %s", err)
	}
	defer file.Close()

	_, err = file.WriteString(appId)
	if err != nil {
		return fmt.Errorf("write: an error occurred while spawning the namespace: %s", err)
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
	if len(layersList) == 0 {
		return fmt.Errorf("mount:layers: no layers specified")
	}

	layerDirs := layerDirectories(layersDir, layersList)
	layersDirs := strings.Join(layerDirs, ":")

	err := tools.MountOverlay(rootFs, layersDirs, filepath.Join(stateDir, "up"), filepath.Join(stateDir, "work"))
	if err != nil {
		return fmt.Errorf("mount:layers %s: an error occurred while spawning the namespace: %s", layersDirs, err)
	}
	return nil
}

func layerDirectories(layersDir string, layers []string) []string {
	directories := make([]string, 0, len(layers))
	for i := len(layers) - 1; i >= 0; i-- {
		directories = append(directories, filepath.Join(layersDir, layers[i]))
	}
	return directories
}

func (c *SpawnCmd) setupBuildMountPoints(rootFs string) error {
	if err := tools.MountTmpfs(filepath.Join(rootFs, "/tmp")); err != nil {
		return fmt.Errorf("mount:/tmp: an error occurred while building the layer: %s", err)
	}
	for _, mount := range []string{"/proc/", "/sys/"} {
		if err := tools.MountBind(mount, filepath.Join(rootFs, mount)); err != nil {
			return fmt.Errorf("mount:%s: an error occurred while building the layer: %s", mount, err)
		}
	}
	return nil
}

func (c *SpawnCmd) setupMountPoints(userUid int, rootFs string, overrideMounts []string) error {
	c.spawnVerbose("Mounting: /tmp")
	err := tools.MountTmpfs(filepath.Join(rootFs, "/tmp"))
	if err != nil {
		return fmt.Errorf("mount:/tmp: an error occurred while spawning the namespace: %s", err)
	}

	mounts := []string{
		"/proc/",
		"/sys/",
	}
	mounts = append(mounts, overrideMounts...)

	for _, mount := range mounts {
		c.spawnVerbose("(override) Mounting: ", mount)

		_, err := os.Stat(mount)
		if os.IsNotExist(err) {
			c.spawnVerbose(mount, " does not exist, that's probably unsupported by the host, ignoring")
			continue
		}

		_, err = os.Stat(filepath.Join(rootFs, mount))
		if os.IsNotExist(err) {
			c.spawnVerbose("does not exist ", mount)
			if strings.HasSuffix(mount, "/") {
				c.spawnVerbose("is dir, creating ", mount)
				err = os.MkdirAll(filepath.Join(rootFs, mount), 0755)
				if err != nil {
					return fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			} else {
				c.spawnVerbose("is file, creating ", mount)
				parentDir := filepath.Dir(mount)
				c.spawnVerbose("parentDir ", parentDir)
				err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
				if err != nil {
					return fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", parentDir, err)
				}
				c.spawnVerbose("creating file ", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return fmt.Errorf("create:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
				err = file.Close()
				if err != nil {
					return fmt.Errorf("close:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			}
		} else if err == nil {
			c.spawnVerbose("exists ", mount)
			if !strings.HasSuffix(mount, "/") {
				c.spawnVerbose("is file, creating ", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return fmt.Errorf("create:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
				err = file.Close()
				if err != nil {
					return fmt.Errorf("close:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			}
		}

		err = tools.MountBind(mount, filepath.Join(rootFs, mount))
		if err != nil {
			return fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", mount, err)
		}
	}

	cpakSockPath := "/tmp/cpak.sock"
	c.spawnVerbose("Waiting for: ", cpakSockPath, " to be available...")
	for {
		_, err := os.Stat(cpakSockPath)
		if err == nil {
			c.spawnVerbose("Mounting: ", cpakSockPath)
			err = tools.MountBind(cpakSockPath, filepath.Join(rootFs, cpakSockPath))
			if err != nil {
				return fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", cpakSockPath, err)
			}
			break
		}
	}

	return nil
}

func (c *SpawnCmd) injectConfigurationFiles(rootFs string) error {
	nvidiaLibs, err := cpak.GetNvidiaLibs()
	if err != nil {
		return fmt.Errorf("an error occurred while spawning the namespace: %s", err)
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
			return fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", parentDir, err)
		}

		c.spawnVerbose("Mounting: ", conf)
		err = tools.MountBind(conf, filepath.Join(rootFs, conf))
		if err != nil {
			return fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
	}

	for _, lib := range nvidiaLibs {
		c.spawnVerbose("Mounting: ", lib)
		if err = tools.MountBind(lib, filepath.Join(rootFs, lib)); err != nil {
			return fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", lib, err)
		}
	}

	nvidiaLibraryDirs := cpak.GetNvidiaLibraryDirs(nvidiaLibs)
	if len(nvidiaLibraryDirs) > 0 {
		ldConfigDir := filepath.Join(rootFs, "/etc/ld.so.conf.d")
		if err = os.MkdirAll(ldConfigDir, 0755); err != nil {
			return fmt.Errorf("mkdir:/etc/ld.so.conf.d: an error occurred while spawning the namespace: %s", err)
		}
		ldConfig := strings.Join(nvidiaLibraryDirs, "\n") + "\n"
		if err = os.WriteFile(filepath.Join(ldConfigDir, "cpak-nvidia.conf"), []byte(ldConfig), 0644); err != nil {
			return fmt.Errorf("write:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
		}
	}

	err = tools.MountBind("/", filepath.Join(rootFs, "/run/host"))
	if err != nil {
		return fmt.Errorf("mount:/: an error occurred while spawning the namespace: %s", err)
	}

	return nil
}

func (c *SpawnCmd) setupExtraLinks(rootFs string, extraLinks []string) error {
	for _, link := range extraLinks {
		linkParts := strings.SplitN(link, ":", 2)
		if len(linkParts) != 2 {
			return fmt.Errorf("invalid link format: an error occurred while spawning the namespace: %s", link)
		}

		c.spawnVerbose("Linking: ", linkParts[0], " ", linkParts[1])
		err := tools.MountBind(linkParts[0], filepath.Join(rootFs, linkParts[1]))
		if err != nil {
			return fmt.Errorf("mount:%s:%s: an error occurred while spawning the namespace: %s", linkParts[0], linkParts[1], err)
		}
	}
	return nil
}

func (c *SpawnCmd) installRuntimePackages(packages []string) error {
	if len(packages) == 0 {
		return fmt.Errorf("no runtime packages specified")
	}
	args := append([]string{"--install"}, packages...)
	cmd := exec.Command("dpkg", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dpkg failed to install runtime packages: %w", err)
	}
	return nil
}

func (c *SpawnCmd) pivotRoot(rootFs string) error {
	c.spawnVerbose("Pivoting: ", rootFs)
	pivotDir := filepath.Join(rootFs, ".pivot_root")
	err := os.MkdirAll(pivotDir, 0755)
	if err != nil {
		return fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", pivotDir, err)
	}

	err = syscall.PivotRoot(rootFs, pivotDir)
	if err != nil {
		return fmt.Errorf("pivot_root: an error occurred while spawning the namespace: %s", err)
	}

	err = os.Chdir("/")
	if err != nil {
		return fmt.Errorf("chdir: an error occurred while spawning the namespace: %s", err)
	}
	return nil
}

func (c *SpawnCmd) startSleepProcess(cmdArgs []string, envVars []string) error {
	c.spawnVerbose("Reconfiguring dynamic linker run-time bindings")
	l := exec.Command("/sbin/ldconfig")
	err := l.Run()
	if err != nil {
		return fmt.Errorf("ldconfig: an error occurred while spawning the namespace: %s", err)
	}

	c.spawnVerbose("Starting sleep process")
	args := []string{}
	if len(cmdArgs) > 0 {
		args = append(args, cmdArgs...)
	} else {
		args = append(args, "/bin/sleep")
		args = append(args, "infinity")
	}

	envv := append(os.Environ(), envVars...)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = envv

	for _, env := range envv {
		if strings.HasPrefix(env, "CPAK_") {
			c.spawnVerbose("CPAK env var found: ", env)
		}
	}

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("start: an error occurred while spawning the namespace: %s", err)
	}

	err = cmd.Process.Release()
	if err != nil {
		return fmt.Errorf("release: an error occurred while spawning the namespace: %s", err)
	}

	return nil
}

func (c *SpawnCmd) createHostExecShimAndLinks(rootFs string, allowedCmds []string) error {
	shimFilePath := filepath.Join(rootFs, strings.TrimPrefix(hostExecShimPath, "/"))
	shimDir := filepath.Dir(shimFilePath)

	c.spawnVerbose("Creating hostexec shim directory: ", shimDir)
	if err := os.MkdirAll(shimDir, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create shim dir %s: an error occurred while spawning the namespace: %s", shimDir, err)
	}

	content, err := cpak.RenderShim(cpakInContainerPath)
	if err != nil {
		return fmt.Errorf("render shim template: an error occurred while spawning the namespace: %s", err)
	}
	if err := os.WriteFile(shimFilePath, content, 0755); err != nil {
		return fmt.Errorf("write shim file %s: an error occurred while spawning the namespace: %s", shimFilePath, err)
	}

	linkTargetDir := filepath.Join(rootFs, "/usr/bin")
	c.spawnVerbose("Creating symlink directory: ", linkTargetDir)
	if err := os.MkdirAll(linkTargetDir, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create link target dir %s: an error occurred while spawning the namespace: %s", linkTargetDir, err)
	}

	for _, cmdName := range allowedCmds {
		if cmdName == "" {
			continue
		}
		linkPath := filepath.Join(linkTargetDir, cmdName)
		relShimPath, err := filepath.Rel(linkTargetDir, shimFilePath)
		if err != nil {
			return fmt.Errorf("calculate relative path for shim from %s: an error occurred while spawning the namespace: %s", linkTargetDir, err)
		}

		c.spawnVerbose("Creating symlink: ", linkPath, " -> ", relShimPath)
		_ = os.Remove(linkPath)
		err = os.Symlink(relShimPath, linkPath)
		if err != nil {
			return fmt.Errorf("create symlink %s -> %s: an error occurred while spawning the namespace: %s", linkPath, relShimPath, err)
		}
	}

	return nil
}
