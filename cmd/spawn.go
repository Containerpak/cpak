/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
	"github.com/mirkobrombin/cpak/pkg/sandbox"
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
	ReadyFd        int      `cli:"ready-fd" help:"write readiness to this file descriptor"`
	ExecSocket     string   `cli:"exec-socket" help:"container command socket"`
	IdleTime       int      `cli:"idle-time" help:"idle timeout in minutes"`
	MountHostRoot  bool     `cli:"mount-host-root" help:"mount the host root read-only at /run/host"`
	Nvidia         bool     `cli:"nvidia" help:"mount the host NVIDIA userspace driver"`
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
		if _, err = c.setupExtraLinks(c.Rootfs, c.ExtraLinks); err != nil {
			return err
		}
		if err = c.pivotRoot(c.Rootfs); err != nil {
			return err
		}
		return c.installRuntimePackages(c.RuntimePackage)
	}

	grants, err := c.setupMountPoints(c.UserUid, c.Rootfs, c.MountOverrides, hostExecSocketPath, c.MountHostRoot)
	if err != nil {
		return err
	}

	configurationGrants, err := c.injectConfigurationFiles(c.Rootfs, c.Nvidia)
	if err != nil {
		return err
	}
	grants = append(grants, configurationGrants...)

	linkGrants, err := c.setupExtraLinks(c.Rootfs, c.ExtraLinks)
	if err != nil {
		return err
	}
	grants = append(grants, linkGrants...)

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

	err = c.createCpakFile(c.AppId, c.Rootfs)
	if err != nil {
		return err
	}

	listener, err := c.createRuntimeListener()
	if err != nil {
		return err
	}
	defer listener.Close()

	err = c.pivotRoot(c.Rootfs)
	if err != nil {
		return err
	}

	_envVars := setEnvironmentVariables(c.ContainerId, c.Rootfs, finalEnvVarsForContainer, c.StateDir, c.LayersDir, c.Layers)
	err = c.serveInit(listener, _envVars, append([]sandbox.PathGrant{{Path: "/", ReadOnly: true}}, grants...), time.Duration(c.IdleTime)*time.Minute)
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
	if _, err := c.setupBaseDevices(rootFs); err != nil {
		return err
	}
	procPath := filepath.Join(rootFs, "/proc")
	if err := os.MkdirAll(procPath, 0755); err != nil {
		return fmt.Errorf("mkdir:/proc: an error occurred while building the layer: %s", err)
	}
	if err := syscall.Mount("proc", procPath, "proc", syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, ""); err != nil {
		return fmt.Errorf("mount:/proc: an error occurred while building the layer: %s", err)
	}
	if err := tools.MountBindReadOnly("/sys/", filepath.Join(rootFs, "/sys/"), true); err != nil {
		return fmt.Errorf("mount:/sys: an error occurred while building the layer: %s", err)
	}
	return nil
}

func (c *SpawnCmd) setupMountPoints(userUid int, rootFs string, overrideMounts []string, hostExecSocketPath string, mountHostRoot bool) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{
		{Path: "/tmp"},
		{Path: "/dev"},
		{Path: "/proc", ReadOnly: true},
		{Path: "/sys", ReadOnly: true},
	}
	c.spawnVerbose("Mounting: /tmp")
	err := tools.MountTmpfs(filepath.Join(rootFs, "/tmp"))
	if err != nil {
		return nil, fmt.Errorf("mount:/tmp: an error occurred while spawning the namespace: %s", err)
	}
	deviceGrants, err := c.setupBaseDevices(rootFs)
	if err != nil {
		return nil, err
	}
	grants = append(grants, deviceGrants...)

	procPath := filepath.Join(rootFs, "/proc")
	if err = os.MkdirAll(procPath, 0755); err != nil {
		return nil, fmt.Errorf("mkdir:/proc: an error occurred while spawning the namespace: %s", err)
	}
	if err = syscall.Mount("proc", procPath, "proc", syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, ""); err != nil {
		return nil, fmt.Errorf("mount:/proc: an error occurred while spawning the namespace: %s", err)
	}
	if err = tools.MountBindReadOnly("/sys/", filepath.Join(rootFs, "/sys/"), true); err != nil {
		return nil, fmt.Errorf("mount:/sys: an error occurred while spawning the namespace: %s", err)
	}
	if mountHostRoot {
		destination := filepath.Join(rootFs, "/run/host")
		if err = os.MkdirAll(destination, 0755); err != nil {
			return nil, fmt.Errorf("mkdir:/run/host: %w", err)
		}
		if err = tools.MountBindReadOnly("/", destination, true); err != nil {
			return nil, fmt.Errorf("mount:/run/host: %w", err)
		}
		grants = append(grants, sandbox.PathGrant{Path: "/run/host", ReadOnly: true})
	}

	for _, mount := range overrideMounts {
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
					return nil, fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			} else {
				c.spawnVerbose("is file, creating ", mount)
				parentDir := filepath.Dir(mount)
				c.spawnVerbose("parentDir ", parentDir)
				err = os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
				if err != nil {
					return nil, fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", parentDir, err)
				}
				c.spawnVerbose("creating file ", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return nil, fmt.Errorf("create:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
				err = file.Close()
				if err != nil {
					return nil, fmt.Errorf("close:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			}
		} else if err == nil {
			c.spawnVerbose("exists ", mount)
			if !strings.HasSuffix(mount, "/") {
				c.spawnVerbose("is file, creating ", mount)
				file, err := os.Create(filepath.Join(rootFs, mount))
				if err != nil {
					return nil, fmt.Errorf("create:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
				err = file.Close()
				if err != nil {
					return nil, fmt.Errorf("close:%s: an error occurred while spawning the namespace: %s", mount, err)
				}
			}
		}

		if filepath.Clean(mount) == "/etc" {
			err = tools.MountBindReadOnly(mount, filepath.Join(rootFs, mount), true)
		} else {
			err = tools.MountBind(mount, filepath.Join(rootFs, mount))
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", mount, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: filepath.Clean(mount), ReadOnly: filepath.Clean(mount) == "/etc"})
	}

	cpakSockPath := "/tmp/cpak.sock"
	if _, statErr := os.Stat(cpakSockPath); statErr == nil {
		c.spawnVerbose("Mounting: ", cpakSockPath)
		if err = tools.MountBind(cpakSockPath, filepath.Join(rootFs, cpakSockPath)); err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", cpakSockPath, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: cpakSockPath})
	}
	if hostExecSocketPath != "" {
		if _, statErr := os.Stat(hostExecSocketPath); statErr != nil {
			return nil, fmt.Errorf("hostexec socket is unavailable: %w", statErr)
		}
		destination := filepath.Join(rootFs, hostExecSocketPath)
		if !tools.IsSameFile(hostExecSocketPath, destination) {
			err = tools.MountBind(hostExecSocketPath, destination)
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", hostExecSocketPath, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: hostExecSocketPath})
	}

	return grants, nil
}

func (c *SpawnCmd) setupBaseDevices(rootFs string) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{}
	deviceRoot := filepath.Join(rootFs, "/dev")
	if err := tools.MountTmpfs(deviceRoot); err != nil {
		return nil, fmt.Errorf("mount:/dev: an error occurred while spawning the namespace: %s", err)
	}
	for _, device := range []string{"null", "zero", "random", "urandom", "tty"} {
		source := filepath.Join("/dev", device)
		destination := filepath.Join(deviceRoot, device)
		if err := tools.MountBind(source, destination); err != nil {
			return nil, fmt.Errorf("mount:/dev/%s: an error occurred while spawning the namespace: %s", device, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: filepath.Join("/dev", device)})
	}
	for name, target := range map[string]string{
		"fd":     "/proc/self/fd",
		"stdin":  "/proc/self/fd/0",
		"stdout": "/proc/self/fd/1",
		"stderr": "/proc/self/fd/2",
	} {
		if err := os.Symlink(target, filepath.Join(deviceRoot, name)); err != nil {
			return nil, fmt.Errorf("link:/dev/%s: an error occurred while spawning the namespace: %s", name, err)
		}
	}
	return grants, nil
}

func (c *SpawnCmd) injectConfigurationFiles(rootFs string, includeNvidia bool) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{}
	var err error
	nvidiaMounts := []cpak.NvidiaMount{}
	if includeNvidia {
		nvidiaMounts, err = cpak.GetNvidiaMounts(rootFs)
		if err != nil {
			return nil, fmt.Errorf("an error occurred while spawning the namespace: %s", err)
		}
	}

	files := []string{
		"/etc/resolv.conf",
		"/etc/hosts",
		"/etc/passwd",
		"/etc/group",
	}

	for _, conf := range files {
		content, readErr := os.ReadFile(conf)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read:%s: an error occurred while spawning the namespace: %s", conf, readErr)
		}
		parentDir := filepath.Dir(conf)
		err := os.MkdirAll(filepath.Join(rootFs, parentDir), 0755)
		if err != nil {
			return nil, fmt.Errorf("mkdir:%s: an error occurred while spawning the namespace: %s", parentDir, err)
		}

		destination := filepath.Join(rootFs, conf)
		if info, statErr := os.Lstat(destination); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			if err = os.Remove(destination); err != nil {
				return nil, fmt.Errorf("remove:%s: an error occurred while spawning the namespace: %s", conf, err)
			}
		}
		c.spawnVerbose("Writing: ", conf)
		if err = os.WriteFile(destination, content, 0644); err != nil {
			return nil, fmt.Errorf("write:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		if err = tools.MountBindReadOnly(destination, destination, true); err != nil {
			return nil, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: conf, ReadOnly: true})
	}

	for _, mount := range nvidiaMounts {
		c.spawnVerbose("Mounting: ", mount.Source, " as ", mount.Destination)
		destination := filepath.Join(rootFs, mount.Destination)
		if mount.RewriteLibraryPath {
			if err = writeNvidiaLoaderConfiguration(mount.Source, destination); err != nil {
				return nil, err
			}
			if err = tools.MountBindReadOnly(destination, destination, true); err != nil {
				return nil, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", mount.Destination, err)
			}
		} else if err = tools.MountBindReadOnly(mount.Source, destination, false); err != nil {
			return nil, fmt.Errorf("mount:%s:%s: an error occurred while spawning the namespace: %s", mount.Source, mount.Destination, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: mount.Destination, ReadOnly: true})
	}

	if len(nvidiaMounts) > 0 {
		ldConfigDir := filepath.Join(rootFs, "/etc/ld.so.conf.d")
		if err = os.MkdirAll(ldConfigDir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir:/etc/ld.so.conf.d: an error occurred while spawning the namespace: %s", err)
		}
		ldConfig := strings.Join(cpak.NvidiaLibraryDirs(), "\n") + "\n"
		if err = os.WriteFile(filepath.Join(ldConfigDir, "cpak-nvidia.conf"), []byte(ldConfig), 0644); err != nil {
			return nil, fmt.Errorf("write:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
		}
	}

	return grants, nil
}

var absoluteNvidiaLibraryPath = regexp.MustCompile(`("library_path"\s*:\s*")/[^"/]*/(?:[^"/]*/)*([^"/]+")`)

func writeNvidiaLoaderConfiguration(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read:%s: an error occurred while configuring NVIDIA: %s", source, err)
	}
	data = absoluteNvidiaLibraryPath.ReplaceAll(data, []byte("${1}${2}"))
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("mkdir:%s: an error occurred while configuring NVIDIA: %s", filepath.Dir(destination), err)
	}
	if err := os.WriteFile(destination, data, 0644); err != nil {
		return fmt.Errorf("write:%s: an error occurred while configuring NVIDIA: %s", destination, err)
	}
	return nil
}

func (c *SpawnCmd) setupExtraLinks(rootFs string, extraLinks []string) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{}
	for _, link := range extraLinks {
		linkParts := strings.SplitN(link, ":", 2)
		if len(linkParts) != 2 {
			return nil, fmt.Errorf("invalid link format: an error occurred while spawning the namespace: %s", link)
		}

		c.spawnVerbose("Linking: ", linkParts[0], " ", linkParts[1])
		err := tools.MountBindReadOnly(linkParts[0], filepath.Join(rootFs, linkParts[1]), false)
		if err != nil {
			return nil, fmt.Errorf("mount:%s:%s: an error occurred while spawning the namespace: %s", linkParts[0], linkParts[1], err)
		}
		grants = append(grants, sandbox.PathGrant{Path: linkParts[1], ReadOnly: true})
	}
	return grants, nil
}

func (c *SpawnCmd) installRuntimePackages(packages []string) error {
	if len(packages) == 0 {
		return fmt.Errorf("no runtime packages specified")
	}
	cmd := runtimePackageCommand(packages)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dpkg failed to install runtime packages: %w", err)
	}
	return nil
}

func runtimePackageCommand(packages []string) *exec.Cmd {
	args := append([]string{"--install"}, packages...)
	cmd := exec.Command("/usr/bin/dpkg", args...)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}
	return cmd
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
	if err = syscall.Unmount("/.pivot_root", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount:/.pivot_root: an error occurred while spawning the namespace: %s", err)
	}
	if err = os.Remove("/.pivot_root"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove:/.pivot_root: an error occurred while spawning the namespace: %s", err)
	}
	return nil
}

func (c *SpawnCmd) createRuntimeListener() (*net.UnixListener, error) {
	if c.ExecSocket == "" {
		return nil, fmt.Errorf("exec socket is required")
	}
	if err := os.MkdirAll(filepath.Dir(c.ExecSocket), 0700); err != nil {
		return nil, fmt.Errorf("create exec socket directory: %w", err)
	}
	if err := os.Remove(c.ExecSocket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale exec socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.ExecSocket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on exec socket: %w", err)
	}
	if err = os.Chmod(c.ExecSocket, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("restrict exec socket: %w", err)
	}
	return listener, nil
}

func (c *SpawnCmd) serveInit(listener *net.UnixListener, envVars []string, grants []sandbox.PathGrant, idleTimeout time.Duration) error {
	c.spawnVerbose("Reconfiguring dynamic linker run-time bindings")
	if _, err := os.Stat("/sbin/ldconfig"); err == nil {
		l := exec.Command("/sbin/ldconfig")
		if err = l.Run(); err != nil {
			return fmt.Errorf("ldconfig: an error occurred while spawning the namespace: %s", err)
		}
	}
	version, err := sandbox.ApplyLandlock(grants)
	if err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			c.Logger.Warning("Landlock is unavailable; continuing without filesystem restrictions")
		} else {
			return err
		}
	} else {
		c.spawnVerbose("Landlock ABI: ", version)
	}
	for _, env := range envVars {
		if strings.HasPrefix(env, "CPAK_") {
			c.spawnVerbose("CPAK env var found: ", env)
		}
	}
	if err := c.signalReady(); err != nil {
		return err
	}
	c.spawnVerbose("Container init is ready")
	lastActivity := time.Now()
	var active atomic.Int64
	var completed atomic.Int64
	for {
		if idleTimeout > 0 {
			lastCompleted := time.Unix(0, completed.Load())
			if lastCompleted.After(lastActivity) {
				lastActivity = lastCompleted
			}
			deadline := lastActivity.Add(idleTimeout)
			if active.Load() > 0 || deadline.Before(time.Now()) {
				deadline = time.Now().Add(idleTimeout)
			}
			if err := listener.SetDeadline(deadline); err != nil {
				return fmt.Errorf("set runtime listener deadline: %w", err)
			}
		}
		connection, err := listener.AcceptUnix()
		if err != nil {
			var netErr net.Error
			if idleTimeout > 0 && errors.As(err, &netErr) && netErr.Timeout() {
				lastCompleted := time.Unix(0, completed.Load())
				if lastCompleted.After(lastActivity) {
					lastActivity = lastCompleted
				}
				if active.Load() == 0 && time.Since(lastActivity) >= idleTimeout {
					c.spawnVerbose("Stopping idle container")
					return nil
				}
				continue
			}
			return fmt.Errorf("accept runtime command: %w", err)
		}
		lastActivity = time.Now()
		active.Add(1)
		go func() {
			defer active.Add(-1)
			defer func() { completed.Store(time.Now().UnixNano()) }()
			c.handleRuntimeConnection(connection, envVars)
		}()
	}
}

func (c *SpawnCmd) signalReady() error {
	if c.ReadyFd > 0 {
		ready := os.NewFile(uintptr(c.ReadyFd), "cpak-ready")
		if ready == nil {
			return fmt.Errorf("ready file descriptor %d is invalid", c.ReadyFd)
		}
		if _, err := ready.Write([]byte{1}); err != nil {
			_ = ready.Close()
			return fmt.Errorf("signal readiness: %w", err)
		}
		if err := ready.Close(); err != nil {
			return fmt.Errorf("close readiness descriptor: %w", err)
		}
	}
	return nil
}

type runtimeOutputWriter struct {
	writer *runtimeproto.Writer
}

func (w runtimeOutputWriter) Write(payload []byte) (int, error) {
	if err := w.writer.Write(runtimeproto.FrameOutput, payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *SpawnCmd) handleRuntimeConnection(connection *net.UnixConn, baseEnv []string) {
	defer connection.Close()
	kind, payload, err := runtimeproto.Read(connection)
	writer := runtimeproto.NewWriter(connection)
	if err != nil || kind != runtimeproto.FrameRequest {
		_ = writer.Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(125))
		return
	}
	request, err := runtimeproto.DecodeRequest(payload)
	if err != nil {
		_ = writer.Write(runtimeproto.FrameOutput, []byte(err.Error()+"\n"))
		_ = writer.Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(125))
		return
	}

	args := append([]string{"launch", "--"}, request.Args...)
	command := exec.Command(cpakInContainerPath, args...)
	command.Env = append(append([]string{}, baseEnv...), request.Env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if !request.AsRoot {
		command.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER
		command.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 1000, HostID: 0, Size: 1}}
		command.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 1000, HostID: 0, Size: 1}}
		command.SysProcAttr.GidMappingsEnableSetgroups = false
		command.SysProcAttr.Credential = &syscall.Credential{Uid: 1000, Gid: 1000}
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = writer.Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(125))
		return
	}
	output := runtimeOutputWriter{writer: writer}
	command.Stdout = output
	command.Stderr = output
	if err = command.Start(); err != nil {
		_ = writer.Write(runtimeproto.FrameOutput, []byte(err.Error()+"\n"))
		_ = writer.Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(127))
		return
	}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer stdin.Close()
		for {
			frameKind, framePayload, readErr := runtimeproto.Read(connection)
			if readErr != nil {
				_ = command.Process.Kill()
				return
			}
			switch frameKind {
			case runtimeproto.FrameInput:
				if _, writeErr := stdin.Write(framePayload); writeErr != nil {
					return
				}
			case runtimeproto.FrameInputClose:
				return
			}
		}
	}()

	waitErr := command.Wait()
	_ = stdin.Close()
	code := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 125
		}
	}
	_ = writer.Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(code))
	_ = connection.CloseWrite()
	select {
	case <-inputDone:
	default:
	}
	_, _ = io.Copy(io.Discard, connection)
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
