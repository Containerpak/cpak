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
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

const cpakInContainerPath = "/usr/local/bin/cpak"
const hostExecShimPath = "/usr/local/bin/cpak-hostexec-shim"
const systemBrokerShimPath = "/usr/local/bin/cpak-system-broker-shim"

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
	Filesystem     []string `cli:"filesystem" help:"encoded filesystem permission"`
	MountOverrides []string `cli:"mount-overrides,m" help:"set the mount overrides"`
	MountShims     []string `cli:"mount-shims,M" help:"set the mount shims"`
	SystemShims    []string `cli:"system-shims" help:"set the system integration shims"`
	ExtraLinks     []string `cli:"extra-links,x" help:"set the extra links"`
	ReadyFd        int      `cli:"ready-fd" help:"write readiness to this file descriptor"`
	ExecSocket     string   `cli:"exec-socket" help:"container command socket"`
	IdleTime       int      `cli:"idle-time" help:"idle timeout in minutes"`
	MountHostRoot  bool     `cli:"mount-host-root" help:"mount the host root read-only at /run/host"`
	Nvidia         bool     `cli:"nvidia" help:"mount the host NVIDIA userspace driver"`
	UserNamespaces bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
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

	filesystem, err := decodeFilesystemPermissions(c.Filesystem)
	if err != nil {
		return err
	}
	grants, err := c.setupMountPoints(c.UserUid, c.Rootfs, c.MountOverrides, filesystem, hostExecSocketPath, c.MountHostRoot)
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
	if len(c.SystemShims) > 0 {
		if err := c.createSystemBrokerShimAndLinks(c.Rootfs, c.SystemShims); err != nil {
			return err
		}
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

	_, err := prepareRootfsDirectory(rootFs, "/tmp")
	if err != nil {
		return fmt.Errorf("mkdir:/tmp: an error occurred while spawning the namespace: %s", err)
	}
	destination, err := prepareRootfsFile(rootFs, "/tmp/.cpak")
	if err != nil {
		return fmt.Errorf("create:/tmp/.cpak: an error occurred while spawning the namespace: %s", err)
	}
	file, err := os.Create(destination)
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

func prepareRootfsDirectory(rootFs, target string) (string, error) {
	return tools.PrepareRootfsTarget(rootFs, target, tools.RootfsTargetDirectory)
}

func prepareRootfsFile(rootFs, target string) (string, error) {
	return tools.PrepareRootfsTarget(rootFs, target, tools.RootfsTargetFile)
}

func prepareRootfsMountTarget(rootFs, target, source string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	kind := tools.RootfsTargetFile
	if info.IsDir() {
		kind = tools.RootfsTargetDirectory
	}
	return tools.PrepareRootfsTarget(rootFs, target, kind)
}

func prepareRootfsBindTarget(rootFs, target, source string) (string, bool, error) {
	destination := filepath.Join(rootFs, target)
	if tools.IsSameFile(source, destination) {
		return destination, false, nil
	}
	destination, err := prepareRootfsMountTarget(rootFs, target, source)
	return destination, true, err
}

func layerDirectories(layersDir string, layers []string) []string {
	directories := make([]string, 0, len(layers))
	for i := len(layers) - 1; i >= 0; i-- {
		directories = append(directories, filepath.Join(layersDir, layers[i]))
	}
	return directories
}

func (c *SpawnCmd) setupBuildMountPoints(rootFs string) error {
	tmpPath, err := prepareRootfsDirectory(rootFs, "/tmp")
	if err != nil {
		return fmt.Errorf("mkdir:/tmp: an error occurred while building the layer: %s", err)
	}
	if err := tools.MountTmpfsPrepared(tmpPath); err != nil {
		return fmt.Errorf("mount:/tmp: an error occurred while building the layer: %s", err)
	}
	if _, err := c.setupBaseDevices(rootFs); err != nil {
		return err
	}
	procPath, err := prepareRootfsDirectory(rootFs, "/proc")
	if err != nil {
		return fmt.Errorf("mkdir:/proc: an error occurred while building the layer: %s", err)
	}
	if err := syscall.Mount("proc", procPath, "proc", syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, ""); err != nil {
		return fmt.Errorf("mount:/proc: an error occurred while building the layer: %s", err)
	}
	sysPath, err := prepareRootfsDirectory(rootFs, "/sys")
	if err != nil {
		return fmt.Errorf("mkdir:/sys: an error occurred while building the layer: %s", err)
	}
	if err := tools.MountBindReadOnlyPrepared("/sys/", sysPath, true); err != nil {
		return fmt.Errorf("mount:/sys: an error occurred while building the layer: %s", err)
	}
	return nil
}

func (c *SpawnCmd) setupMountPoints(userUid int, rootFs string, overrideMounts []string, filesystem []types.FilesystemPermission, hostExecSocketPath string, mountHostRoot bool) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{
		{Path: "/tmp"},
		{Path: "/dev"},
		{Path: "/proc", ReadOnly: true},
		{Path: "/sys", ReadOnly: true},
	}
	c.spawnVerbose("Mounting: /tmp")
	tmpPath, err := prepareRootfsDirectory(rootFs, "/tmp")
	if err != nil {
		return nil, fmt.Errorf("mkdir:/tmp: an error occurred while spawning the namespace: %s", err)
	}
	err = tools.MountTmpfsPrepared(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("mount:/tmp: an error occurred while spawning the namespace: %s", err)
	}
	deviceGrants, err := c.setupBaseDevices(rootFs)
	if err != nil {
		return nil, err
	}
	grants = append(grants, deviceGrants...)

	procPath, err := prepareRootfsDirectory(rootFs, "/proc")
	if err != nil {
		return nil, fmt.Errorf("mkdir:/proc: an error occurred while spawning the namespace: %s", err)
	}
	if err = syscall.Mount("proc", procPath, "proc", syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV, ""); err != nil {
		return nil, fmt.Errorf("mount:/proc: an error occurred while spawning the namespace: %s", err)
	}
	sysPath, err := prepareRootfsDirectory(rootFs, "/sys")
	if err != nil {
		return nil, fmt.Errorf("mkdir:/sys: an error occurred while spawning the namespace: %s", err)
	}
	if err = tools.MountBindReadOnlyPrepared("/sys/", sysPath, true); err != nil {
		return nil, fmt.Errorf("mount:/sys: an error occurred while spawning the namespace: %s", err)
	}
	if mountHostRoot {
		destination, prepareErr := prepareRootfsDirectory(rootFs, "/run/host")
		if prepareErr != nil {
			return nil, fmt.Errorf("mkdir:/run/host: %w", prepareErr)
		}
		if err = tools.MountBindReadOnlyPrepared("/", destination, true); err != nil {
			return nil, fmt.Errorf("mount:/run/host: %w", err)
		}
		grants = append(grants, sandbox.PathGrant{Path: "/run/host", ReadOnly: true})
	}
	for _, permission := range filesystem {
		grant, mountErr := c.mountFilesystemPermission(rootFs, permission)
		if mountErr != nil {
			return nil, mountErr
		}
		grants = append(grants, grant)
	}

	for _, mount := range overrideMounts {
		c.spawnVerbose("(override) Mounting: ", mount)

		_, err := os.Stat(mount)
		if os.IsNotExist(err) {
			c.spawnVerbose(mount, " does not exist, that's probably unsupported by the host, ignoring")
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat:%s: an error occurred while spawning the namespace: %s", mount, err)
		}
		destination, prepareErr := prepareRootfsMountTarget(rootFs, mount, mount)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare mount:%s: an error occurred while spawning the namespace: %s", mount, prepareErr)
		}

		if filepath.Clean(mount) == "/etc" {
			err = tools.MountBindReadOnlyPrepared(mount, destination, true)
		} else {
			err = tools.MountBindPrepared(mount, destination)
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", mount, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: filepath.Clean(mount), ReadOnly: filepath.Clean(mount) == "/etc"})
	}

	cpakSockSource := os.Getenv("CPAK_SERVICE_SOCKET")
	if cpakSockSource == "" {
		cpakSockSource = "/tmp/cpak.sock"
	}
	cpakSockTarget := "/tmp/cpak.sock"
	if _, statErr := os.Stat(cpakSockSource); statErr == nil {
		c.spawnVerbose("Mounting: ", cpakSockSource)
		destination, needsMount, prepareErr := prepareRootfsBindTarget(rootFs, cpakSockTarget, cpakSockSource)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare mount:%s: an error occurred while spawning the namespace: %s", cpakSockSource, prepareErr)
		}
		if needsMount {
			err = tools.MountBindPrepared(cpakSockSource, destination)
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", cpakSockSource, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: cpakSockTarget})
	}
	if hostExecSocketPath != "" {
		if _, statErr := os.Stat(hostExecSocketPath); statErr != nil {
			return nil, fmt.Errorf("hostexec socket is unavailable: %w", statErr)
		}
		destination, needsMount, prepareErr := prepareRootfsBindTarget(rootFs, hostExecSocketPath, hostExecSocketPath)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare mount:%s: an error occurred while spawning the namespace: %s", hostExecSocketPath, prepareErr)
		}
		if needsMount {
			err = tools.MountBindPrepared(hostExecSocketPath, destination)
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", hostExecSocketPath, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: hostExecSocketPath})
	}
	return grants, nil
}

func decodeFilesystemPermissions(encoded []string) ([]types.FilesystemPermission, error) {
	permissions := make([]types.FilesystemPermission, 0, len(encoded))
	for _, value := range encoded {
		permission, err := types.DecodeFilesystemPermission(value)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	if err := types.ValidateFilesystemPermissions(permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}

func (c *SpawnCmd) mountFilesystemPermission(rootFs string, permission types.FilesystemPermission) (sandbox.PathGrant, error) {
	source, target, err := types.ResolveFilesystemPermission(permission)
	if err != nil {
		return sandbox.PathGrant{}, err
	}
	if _, err := os.Stat(source); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("filesystem path %s is unavailable: %w", source, err)
	}
	destination, err := prepareRootfsMountTarget(rootFs, target, source)
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("prepare filesystem target %s: %w", target, err)
	}
	c.spawnVerbose("(filesystem) Mounting: ", source, " as ", target)
	if permission.Access == "read-only" {
		if err := tools.MountBindReadOnlyPrepared(source, destination, false); err != nil {
			return sandbox.PathGrant{}, fmt.Errorf("mount filesystem %s: %w", source, err)
		}
	} else if err := tools.MountBindPrepared(source, destination); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("mount filesystem %s: %w", source, err)
	}
	return sandbox.PathGrant{Path: target, ReadOnly: permission.Access == "read-only"}, nil
}

func (c *SpawnCmd) setupBaseDevices(rootFs string) ([]sandbox.PathGrant, error) {
	grants := []sandbox.PathGrant{}
	deviceRoot, err := prepareRootfsDirectory(rootFs, "/dev")
	if err != nil {
		return nil, fmt.Errorf("mkdir:/dev: an error occurred while spawning the namespace: %s", err)
	}
	if err := tools.MountTmpfsPrepared(deviceRoot); err != nil {
		return nil, fmt.Errorf("mount:/dev: an error occurred while spawning the namespace: %s", err)
	}
	for _, device := range []string{"null", "zero", "random", "urandom", "tty"} {
		source := filepath.Join("/dev", device)
		destination, prepareErr := prepareRootfsMountTarget(rootFs, filepath.Join("/dev", device), source)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare:/dev/%s: an error occurred while spawning the namespace: %s", device, prepareErr)
		}
		if err := tools.MountBindPrepared(source, destination); err != nil {
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
		destination, prepareErr := prepareRootfsFile(rootFs, conf)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare:%s: an error occurred while spawning the namespace: %s", conf, prepareErr)
		}
		c.spawnVerbose("Writing: ", conf)
		if err = os.WriteFile(destination, content, 0644); err != nil {
			return nil, fmt.Errorf("write:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		if err = os.Chmod(destination, 0644); err != nil {
			return nil, fmt.Errorf("chmod:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		if err = tools.MountBindReadOnlyPrepared(destination, destination, true); err != nil {
			return nil, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: conf, ReadOnly: true})
	}

	for _, mount := range nvidiaMounts {
		c.spawnVerbose("Mounting: ", mount.Source, " as ", mount.Destination)
		destination, prepareErr := prepareRootfsMountTarget(rootFs, mount.Destination, mount.Source)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare:%s: an error occurred while spawning the namespace: %s", mount.Destination, prepareErr)
		}
		if mount.RewriteLibraryPath {
			if err = writeNvidiaLoaderConfiguration(mount.Source, destination); err != nil {
				return nil, err
			}
			if err = tools.MountBindReadOnlyPrepared(destination, destination, true); err != nil {
				return nil, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", mount.Destination, err)
			}
		} else if err = tools.MountBindReadOnlyPrepared(mount.Source, destination, false); err != nil {
			return nil, fmt.Errorf("mount:%s:%s: an error occurred while spawning the namespace: %s", mount.Source, mount.Destination, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: mount.Destination, ReadOnly: true})
	}

	if len(nvidiaMounts) > 0 {
		_, prepareErr := prepareRootfsDirectory(rootFs, "/etc/ld.so.conf.d")
		if prepareErr != nil {
			return nil, fmt.Errorf("mkdir:/etc/ld.so.conf.d: an error occurred while spawning the namespace: %s", prepareErr)
		}
		ldConfig := strings.Join(cpak.NvidiaLibraryDirs(), "\n") + "\n"
		ldConfigPath, prepareErr := prepareRootfsFile(rootFs, "/etc/ld.so.conf.d/cpak-nvidia.conf")
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", prepareErr)
		}
		if err = os.WriteFile(ldConfigPath, []byte(ldConfig), 0644); err != nil {
			return nil, fmt.Errorf("write:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
		}
		if err = os.Chmod(ldConfigPath, 0644); err != nil {
			return nil, fmt.Errorf("chmod:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
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
	if err := os.Chmod(destination, 0644); err != nil {
		return fmt.Errorf("chmod:%s: an error occurred while configuring NVIDIA: %s", destination, err)
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
		destination, prepareErr := prepareRootfsMountTarget(rootFs, linkParts[1], linkParts[0])
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare:%s: an error occurred while spawning the namespace: %s", linkParts[1], prepareErr)
		}
		err := tools.MountBindReadOnlyPrepared(linkParts[0], destination, false)
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
	pivotDir, err := tools.PrepareRootfsTarget(rootFs, "/.pivot_root", tools.RootfsTargetDirectory)
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

	args := []string{"launch"}
	if c.UserNamespaces {
		args = append(args, "--user-namespaces")
	}
	args = append(args, "--")
	args = append(args, request.Args...)
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
	shimFilePath, err := prepareRootfsFile(rootFs, hostExecShimPath)
	if err != nil {
		return fmt.Errorf("prepare shim file %s: an error occurred while spawning the namespace: %s", hostExecShimPath, err)
	}
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
	if err := os.Chmod(shimFilePath, 0755); err != nil {
		return fmt.Errorf("chmod shim file %s: an error occurred while spawning the namespace: %s", shimFilePath, err)
	}

	linkTargetDir, err := prepareRootfsDirectory(rootFs, "/usr/bin")
	if err != nil {
		return fmt.Errorf("prepare link target directory: an error occurred while spawning the namespace: %s", err)
	}
	c.spawnVerbose("Creating symlink directory: ", linkTargetDir)
	if err := os.MkdirAll(linkTargetDir, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create link target dir %s: an error occurred while spawning the namespace: %s", linkTargetDir, err)
	}

	for _, cmdName := range allowedCmds {
		if cmdName == "" {
			continue
		}
		if filepath.Base(cmdName) != cmdName || cmdName == "." || cmdName == ".." {
			return fmt.Errorf("invalid hostexec command name: %s", cmdName)
		}
		linkPath, prepareErr := prepareRootfsFile(rootFs, filepath.Join("/usr/bin", cmdName))
		if prepareErr != nil {
			return fmt.Errorf("prepare link %s: an error occurred while spawning the namespace: %s", cmdName, prepareErr)
		}
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

func (c *SpawnCmd) createSystemBrokerShimAndLinks(rootFs string, shims []string) error {
	shimFilePath, err := prepareRootfsFile(rootFs, systemBrokerShimPath)
	if err != nil {
		return fmt.Errorf("prepare system broker shim: %w", err)
	}
	content, err := cpak.RenderSystemBrokerShim()
	if err != nil {
		return fmt.Errorf("render system broker shim: %w", err)
	}
	if err := os.WriteFile(shimFilePath, content, 0755); err != nil {
		return fmt.Errorf("write system broker shim: %w", err)
	}
	for _, name := range shims {
		if name != "notify-send" && name != "xdg-open" {
			return fmt.Errorf("invalid system broker shim: %s", name)
		}
		linkPath, prepareErr := prepareRootfsFile(rootFs, filepath.Join("/usr/local/bin", name))
		if prepareErr != nil {
			return fmt.Errorf("prepare system broker link %s: %w", name, prepareErr)
		}
		relative, err := filepath.Rel(filepath.Dir(linkPath), shimFilePath)
		if err != nil {
			return fmt.Errorf("calculate system broker link %s: %w", name, err)
		}
		_ = os.Remove(linkPath)
		if err := os.Symlink(relative, linkPath); err != nil {
			return fmt.Errorf("create system broker link %s: %w", name, err)
		}
	}
	return nil
}
