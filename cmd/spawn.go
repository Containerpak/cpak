/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

const cpakInContainerPath = "/usr/local/bin/cpak"
const systemBrokerShimPath = "/usr/local/bin/cpak-system-broker-shim"
const desktopRuntimeTarget = "/run/cpak/desktop-runtime"

// serviceSocketTarget is where a nested run inside the container looks for the
// cpak service. It is a mount target and nothing else: what is bound onto it
// comes from --service-socket, which the host resolved. The name is the one the
// host writes into the container environment, so it is read from there.
const serviceSocketTarget = cpak.ContainerServiceSocketPath

const openURIHandlerDesktopPath = "/usr/local/share/applications/cpak-open-uri.desktop"
const openURIHandlerDefaultsPath = "/usr/local/etc/xdg/cpak-mimeapps.list"

const openURIHandlerDesktopEntry = `[Desktop Entry]
Type=Application
Name=cpak URI opener
NoDisplay=true
Exec=/usr/local/bin/xdg-open %u
MimeType=x-scheme-handler/http;x-scheme-handler/https;x-scheme-handler/mailto;
`

const openURIHandlerDefaults = `[Default Applications]
x-scheme-handler/http=cpak-open-uri.desktop;
x-scheme-handler/https=cpak-open-uri.desktop;
x-scheme-handler/mailto=cpak-open-uri.desktop;
`

type SpawnCmd struct {
	Verbose            bool     `cli:"verbose,v" help:"enable verbose output"`
	UserUid            int      `cli:"user-uid" help:"set the user uid"`
	AppId              string   `cli:"app-id" help:"set the app id"`
	NestedToken        string   `cli:"nested-token" help:"the capability this container presents to run its declared dependencies"`
	MachineId          string   `cli:"machine-id" help:"set the application machine id"`
	ContainerId        string   `cli:"container-id" help:"set the container id"`
	Rootfs             string   `cli:"rootfs" help:"set the rootfs"`
	Env                []string `cli:"env,e" help:"set environment variables"`
	Layers             string   `cli:"layers" help:"set the layers"`
	StateDir           string   `cli:"state-dir" help:"set the state directory"`
	MaskState          []string `cli:"mask-state" help:"a directory holding cpak's own state, to hide inside any grant that contains it"`
	ImageDir           string   `cli:"image-dir" help:"set the image directory"`
	LayersDir          string   `cli:"layers-dir" help:"set the layers directory"`
	LowerDir           string   `cli:"lower-dir" help:"set the prepared lower directory"`
	Filesystem         []string `cli:"filesystem" help:"encoded filesystem permission"`
	MountOverrides     []string `cli:"mount-overrides,m" help:"set the mount overrides"`
	SystemShims        []string `cli:"system-shims" help:"set the system integration shims"`
	ExtraLinks         []string `cli:"extra-links,x" help:"set the extra links"`
	DesktopRuntime     string   `cli:"desktop-runtime" help:"mount the nested desktop runtime"`
	ReadyFd            int      `cli:"ready-fd" help:"write readiness to this file descriptor"`
	ExecSocket         string   `cli:"exec-socket" help:"container command socket"`
	GrantSocket        string   `cli:"grant-socket" help:"file grant mount socket"`
	ServiceSocket      string   `cli:"service-socket" help:"host socket a nested run of this container reaches"`
	PrivateHome        string   `cli:"private-home" help:"persistent private application home"`
	IdleTime           int      `cli:"idle-time" help:"idle timeout in minutes"`
	MountHostRoot      bool     `cli:"mount-host-root" help:"mount the host root read-only at /run/host"`
	Nvidia             bool     `cli:"nvidia" help:"mount the host NVIDIA userspace driver"`
	UserNamespaces     bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
	AllowPtrace        bool     `cli:"allow-ptrace" help:"allow tracing inside the private process namespace"`
	BuildLayer         bool     `cli:"build-layer" help:"build a managed layer and exit"`
	AllowRoot          bool     `cli:"allow-root" help:"let nested commands run as root inside the container"`
	RuntimePackage     []string `cli:"runtime-package" help:"install a package in the managed layer"`
	RuntimeInstaller   []string `cli:"runtime-installer" help:"select the installer for each runtime package"`
	RuntimeDestination []string `cli:"runtime-destination" help:"select the destination for each runtime package"`
	ExtraArgs          []string `arg:"extra" help:"Extra arguments"`

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

	finalEnvVarsForContainer := []string{}
	for _, envVar := range c.Env {
		finalEnvVarsForContainer = append(finalEnvVarsForContainer, envVar)
	}

	c.spawnVerbose("Remounting as private")
	err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, "")
	if err != nil {
		return fmt.Errorf("mount: an error occurred while spawning the namespace: %s", err)
	}

	err = mountLayers(c.Rootfs, c.LowerDir, c.StateDir)
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
		return c.installRuntimePackages(c.RuntimePackage, c.RuntimeInstaller, c.RuntimeDestination)
	}
	machineIDGrant, err := c.injectMachineID(c.Rootfs, c.MachineId)
	if err != nil {
		return err
	}

	filesystem, err := decodeFilesystemPermissions(c.Filesystem)
	if err != nil {
		return err
	}
	grants, err := c.setupMountPoints(c.UserUid, c.Rootfs, c.MountOverrides, filesystem, c.MountHostRoot)
	if err != nil {
		return err
	}
	grants = append(grants, machineIDGrant)

	configurationGrants, refreshDynamicLinker, err := c.injectConfigurationFiles(c.Rootfs, c.Nvidia)
	if err != nil {
		return err
	}
	grants = append(grants, configurationGrants...)

	linkGrants, err := c.setupExtraLinks(c.Rootfs, c.ExtraLinks)
	if err != nil {
		return err
	}
	grants = append(grants, linkGrants...)
	if c.DesktopRuntime != "" {
		desktopRuntimeGrant, err := c.setupDesktopRuntime(c.Rootfs, c.DesktopRuntime)
		if err != nil {
			return err
		}
		grants = append(grants, desktopRuntimeGrant)
	}

	if len(c.SystemShims) > 0 {
		if err := c.createSystemBrokerShimAndLinks(c.Rootfs, c.SystemShims); err != nil {
			return err
		}
		if containsString(c.SystemShims, "xdg-open") {
			if err := c.installOpenURIHandler(c.Rootfs); err != nil {
				return err
			}
		}
	}

	err = c.createCpakFile(c.NestedToken, c.Rootfs)
	if err != nil {
		return err
	}

	listener, err := c.createRuntimeListener()
	if err != nil {
		return err
	}
	defer listener.Close()
	grantListener, err := c.createGrantListener()
	if err != nil {
		return err
	}
	defer grantListener.Close()
	grantRoot, err := c.setupGrantRoot(c.Rootfs)
	if err != nil {
		return err
	}
	grants = append(grants, grantRoot)
	grantMounts, err := startGrantMountWorker()
	if err != nil {
		return err
	}
	defer grantMounts.Close()

	err = c.pivotRoot(c.Rootfs)
	if err != nil {
		return err
	}

	layersPath := c.LayersDir
	if c.LowerDir != "" {
		layersPath = c.LowerDir
	}
	_envVars := setEnvironmentVariables(c.ContainerId, c.Rootfs, finalEnvVarsForContainer, c.StateDir, layersPath, c.Layers)
	err = c.serveInit(listener, grantListener, grantMounts, _envVars, append([]sandbox.PathGrant{{Path: "/", ReadOnly: true}}, grants...), time.Duration(c.IdleTime)*time.Minute, refreshDynamicLinker)
	if err != nil {
		return err
	}

	return nil
}

func (c *SpawnCmd) injectMachineID(rootFs, machineID string) (sandbox.PathGrant, error) {
	if machineID == "" {
		var err error
		machineID, err = generateMachineID(rand.Reader)
		if err != nil {
			return sandbox.PathGrant{}, fmt.Errorf("generate container machine ID: %w", err)
		}
	}
	if err := validateMachineID(machineID); err != nil {
		return sandbox.PathGrant{}, err
	}
	destination, err := prepareRootfsFile(rootFs, "/etc/machine-id")
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("prepare:/etc/machine-id: %w", err)
	}
	if err := os.Chmod(destination, 0600); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("chmod:/etc/machine-id: %w", err)
	}
	if err := os.WriteFile(destination, []byte(machineID+"\n"), 0444); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("write:/etc/machine-id: %w", err)
	}
	if err := os.Chmod(destination, 0444); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("chmod:/etc/machine-id: %w", err)
	}
	if err := tools.MountBindReadOnlyPrepared(destination, destination, true); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("restrict:/etc/machine-id: %w", err)
	}
	return sandbox.PathGrant{Path: "/etc/machine-id", ReadOnly: true}, nil
}

func generateMachineID(reader io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validateMachineID(value string) error {
	if len(value) != 32 || value != strings.ToLower(value) {
		return errors.New("machine ID must be 32 lowercase hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return errors.New("machine ID must be 32 lowercase hexadecimal characters")
	}
	for _, octet := range decoded {
		if octet != 0 {
			return nil
		}
	}
	return errors.New("machine ID cannot be all zeroes")
}

func setEnvironmentVariables(containerId, rootFs string, envVars []string, stateDir, layersDir, layers string) []string {
	envVars = append(envVars, "CPAK_CONTAINER_ID="+containerId)
	envVars = append(envVars, "CPAK_ROOTFS="+rootFs)
	envVars = append(envVars, "CPAK_STATE_DIR="+stateDir)
	envVars = append(envVars, "CPAK_LAYERS_DIR="+layersDir)
	envVars = append(envVars, "CPAK_LAYERS="+layers)
	return envVars
}

// createCpakFile writes the capability the container presents when it asks to
// run one of its declared dependencies. It used to hold the application
// identifier, which is public metadata and therefore proved nothing.
func (c *SpawnCmd) createCpakFile(token string, rootFs string) error {
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

	_, err = file.WriteString(token)
	if err != nil {
		return fmt.Errorf("write: an error occurred while spawning the namespace: %s", err)
	}

	return nil
}

// mountLayers refuses to compose a root out of anything but the directories it
// was handed. Rebuilding them here from a caller supplied directory would mount
// whatever that argument points at.
func mountLayers(rootFs, lowerDir, stateDir string) error {
	if lowerDir == "" {
		return fmt.Errorf("mount:layers: no prepared layer directories")
	}
	err := tools.MountOverlay(rootFs, lowerDir, filepath.Join(stateDir, "up"), filepath.Join(stateDir, "work"))
	if err != nil {
		return fmt.Errorf("mount:layers %s: an error occurred while spawning the namespace: %s", lowerDir, err)
	}
	return nil
}

func prepareRootfsDirectory(rootFs, target string) (string, error) {
	return tools.PrepareRootfsTarget(rootFs, target, tools.RootfsTargetDirectory)
}

func prepareRootfsFile(rootFs, target string) (string, error) {
	return tools.PrepareRootfsTarget(rootFs, target, tools.RootfsTargetFile)
}

func prepareRootfsConfigurationFile(rootFs, target string) (string, error) {
	return tools.PrepareRootfsReplacementFile(rootFs, target)
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

func (c *SpawnCmd) setupMountPoints(userUid int, rootFs string, overrideMounts []string, filesystem []types.FilesystemPermission, mountHostRoot bool) ([]sandbox.PathGrant, error) {
	grants := baseSandboxGrants(c.UserNamespaces)
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
	if c.PrivateHome != "" {
		privateHomeGrant, mountErr := c.mountPrivateHome(rootFs, c.PrivateHome)
		if mountErr != nil {
			return nil, mountErr
		}
		grants = append(grants, privateHomeGrant)
	}

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
		if err = c.maskCpakState(rootFs, "/", "/run/host", true); err != nil {
			return nil, err
		}
		grants = append(grants, sandbox.PathGrant{Path: "/run/host", ReadOnly: true})
	}
	for _, permission := range filesystem {
		grant, mounted, mountErr := c.mountFilesystemPermission(rootFs, permission)
		if mountErr != nil {
			return nil, mountErr
		}
		if mounted {
			grants = append(grants, grant)
		}
	}

	for _, mount := range overrideMounts {
		c.spawnVerbose("(override) Mounting: ", mount)

		source, found, resolveErr := resolveOverrideMountSource(mount, "/run/host")
		if resolveErr != nil {
			return nil, fmt.Errorf("stat:%s: an error occurred while spawning the namespace: %s", mount, resolveErr)
		}
		if !found {
			c.spawnVerbose(mount, " does not exist, that's probably unsupported by the host, ignoring")
			continue
		}
		if filepath.Clean(mount) == "/tmp/.X11-unix" {
			if err = mountX11Sockets(rootFs, source); err != nil {
				return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", mount, err)
			}
			grants = append(grants, sandbox.PathGrant{Path: "/tmp/.X11-unix"})
			continue
		}
		// A base device is already bound by the time this loop runs, and an
		// override may name the same path: deviceTTY asks for /dev/tty, which
		// setupBaseDevices always binds. Preparing it a second time finds a
		// character device where a regular file is required and refuses, so a
		// permission the schema advertises could never be used. The bind-aware
		// preparation answers that the work is already done, which is the same
		// question the filesystem permissions already ask.
		destination, needed, prepareErr := prepareRootfsBindTarget(rootFs, mount, source)
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare mount:%s: an error occurred while spawning the namespace: %s", mount, prepareErr)
		}
		if !needed {
			c.spawnVerbose(mount, " is already mounted, skipping")
			grants = append(grants, sandbox.PathGrant{Path: filepath.Clean(mount), ReadOnly: filepath.Clean(mount) == "/etc"})
			continue
		}

		if filepath.Clean(mount) == "/etc" {
			err = tools.MountBindReadOnlyPrepared(source, destination, true)
		} else {
			err = tools.MountBindPrepared(source, destination)
		}
		if err != nil {
			return nil, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", mount, err)
		}
		// This is what a legacy fsHostHome produces: a plain mount that never
		// meets the typed grants, and so was bound with no mask at all.
		if err = c.maskCpakState(rootFs, filepath.Clean(mount), filepath.Clean(mount), filepath.Clean(mount) == "/etc"); err != nil {
			return nil, err
		}
		grants = append(grants, sandbox.PathGrant{Path: filepath.Clean(mount), ReadOnly: filepath.Clean(mount) == "/etc"})
	}

	serviceGrant, mounted, err := c.mountServiceSocket(rootFs)
	if err != nil {
		return nil, err
	}
	if mounted {
		grants = append(grants, serviceGrant)
	}
	return grants, nil
}

// mountServiceSocket binds the host socket of the cpak service at the address a
// nested run inside this container dials.
//
// The source is the flag and never CPAK_SERVICE_SOCKET. That variable names the
// container side of this very mount, and the process building the container
// hands it to this one along with everything else the container is to see, so
// reading it here would resolve /tmp/cpak.sock as a host path: the real service
// would go unmounted and whatever another account left at that name would be
// bound in its place.
func (c *SpawnCmd) mountServiceSocket(rootFs string) (sandbox.PathGrant, bool, error) {
	if c.ServiceSocket == "" {
		return sandbox.PathGrant{}, false, nil
	}
	info, err := os.Lstat(c.ServiceSocket)
	if err != nil {
		if os.IsNotExist(err) {
			return sandbox.PathGrant{}, false, nil
		}
		return sandbox.PathGrant{}, false, fmt.Errorf("stat:%s: an error occurred while spawning the namespace: %s", c.ServiceSocket, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return sandbox.PathGrant{}, false, fmt.Errorf("mount:%s: the cpak service socket is not a socket", c.ServiceSocket)
	}
	c.spawnVerbose("Mounting: ", c.ServiceSocket)
	destination, needsMount, err := prepareRootfsBindTarget(rootFs, serviceSocketTarget, c.ServiceSocket)
	if err != nil {
		return sandbox.PathGrant{}, false, fmt.Errorf("prepare mount:%s: an error occurred while spawning the namespace: %s", c.ServiceSocket, err)
	}
	if needsMount {
		if err = tools.MountBindPrepared(c.ServiceSocket, destination); err != nil {
			return sandbox.PathGrant{}, false, fmt.Errorf("mount:%s: an error occurred while spawning the namespace: %s", c.ServiceSocket, err)
		}
	}
	return sandbox.PathGrant{Path: serviceSocketTarget}, true, nil
}

func (c *SpawnCmd) mountPrivateHome(rootFs, source string) (sandbox.PathGrant, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		return sandbox.PathGrant{}, errors.New("resolve private application home")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("inspect private application home: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return sandbox.PathGrant{}, errors.New("private application home is invalid")
	}
	destination, err := prepareRootfsDirectory(rootFs, home)
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("prepare private application home: %w", err)
	}
	if err = tools.MountBindPrepared(source, destination); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("mount private application home: %w", err)
	}
	return sandbox.PathGrant{Path: home}, nil
}

func baseSandboxGrants(userNamespaces bool) []sandbox.PathGrant {
	return []sandbox.PathGrant{
		{Path: "/tmp"},
		{Path: "/dev"},
		{Path: "/proc", ReadOnly: true, WriteFiles: userNamespaces},
		{Path: "/sys", ReadOnly: true},
	}
}

func mountX11Sockets(rootFs, source string) error {
	destination, err := prepareRootfsDirectory(rootFs, "/tmp/.X11-unix")
	if err != nil {
		return err
	}
	if err = os.Chmod(destination, os.ModeSticky|0777); err != nil {
		return err
	}
	sockets, err := x11SocketPaths(source)
	if err != nil {
		return err
	}
	for _, socket := range sockets {
		target, prepareErr := prepareRootfsFile(rootFs, filepath.Join("/tmp/.X11-unix", filepath.Base(socket)))
		if prepareErr != nil {
			return prepareErr
		}
		if err = tools.MountPrepared(socket, target, syscall.MS_BIND); err != nil {
			return err
		}
	}
	return nil
}

func x11SocketPaths(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var sockets []string
	for _, entry := range entries {
		name := entry.Name()
		if !isX11SocketName(name) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if info.Mode()&os.ModeSocket != 0 {
			sockets = append(sockets, filepath.Join(directory, name))
		}
	}
	return sockets, nil
}

func isX11SocketName(name string) bool {
	if len(name) < 2 || name[0] != 'X' {
		return false
	}
	for _, char := range name[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func resolveOverrideMountSource(target, hostRoot string) (string, bool, error) {
	candidates := []string{target}
	if filepath.IsAbs(target) && hostRoot != "" {
		candidates = append(candidates, filepath.Join(hostRoot, strings.TrimPrefix(filepath.Clean(target), "/")))
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
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

func (c *SpawnCmd) mountFilesystemPermission(rootFs string, permission types.FilesystemPermission) (sandbox.PathGrant, bool, error) {
	source, target, err := types.ResolveFilesystemPermission(permission)
	if err != nil {
		if errors.Is(err, types.ErrXDGUserDirectoryUnavailable) {
			c.spawnVerbose("(filesystem) XDG directory is unavailable, ignoring: ", permission.Path)
			return sandbox.PathGrant{}, false, nil
		}
		return sandbox.PathGrant{}, false, err
	}
	// An application installed before cpak refused these still carries one, and
	// the refusal belongs where a grant is granted, not here: what a launch can
	// do is leave the mount out. The application starts, and reaches nothing it
	// should not have been given.
	if types.PathIsCpakState(source, c.maskedStateDirectories()) {
		c.spawnVerbose("(filesystem) Leaving out a grant on cpak's own state: ", permission.Path)
		return sandbox.PathGrant{}, false, nil
	}
	if _, err := os.Stat(source); err != nil {
		if os.IsNotExist(err) && strings.HasPrefix(permission.Path, "xdg-") {
			c.spawnVerbose("(filesystem) XDG directory is unavailable, ignoring: ", source)
			return sandbox.PathGrant{}, false, nil
		}
		return sandbox.PathGrant{}, false, fmt.Errorf("filesystem path %s is unavailable: %w", source, err)
	}
	destination, err := prepareRootfsMountTarget(rootFs, target, source)
	if err != nil {
		return sandbox.PathGrant{}, false, fmt.Errorf("prepare filesystem target %s: %w", target, err)
	}
	c.spawnVerbose("(filesystem) Mounting: ", source, " as ", target)
	if permission.Access == "read-only" {
		if err := tools.MountBindReadOnlyPrepared(source, destination, false); err != nil {
			return sandbox.PathGrant{}, false, fmt.Errorf("mount filesystem %s: %w", source, err)
		}
	} else if err := tools.MountBindPrepared(source, destination); err != nil {
		return sandbox.PathGrant{}, false, fmt.Errorf("mount filesystem %s: %w", source, err)
	}
	if err := c.maskCpakState(rootFs, source, target, permission.Access == "read-only"); err != nil {
		return sandbox.PathGrant{}, false, err
	}
	return sandbox.PathGrant{Path: target, ReadOnly: permission.Access == "read-only"}, true, nil
}

// maskedStateDirectories is where this installation keeps its own state, as
// cpak resolved it and passed it down. The default layout is the fallback for a
// spawn started without the flag, so the protection is never quietly empty.
func (c *SpawnCmd) maskedStateDirectories() []string {
	if len(c.MaskState) > 0 {
		return c.MaskState
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return types.CpakStateDirectories(home, filepath.Join(home, ".local", "share", "cpak"))
}

// maskedCpakPath is one piece of cpak's own state as a grant exposes it: where
// it lives on the host, and where the container would read it.
type maskedCpakPath struct {
	host      string
	container string
	directory bool
}

// cpakStateMasks maps cpak's own state onto the container side of a grant that
// binds source at target. The two differ, and that difference is the hole: the
// host scope binds / at /run/host, where the store of every other application
// is a subdirectory away, so asking where the state lives on the host answers
// the wrong question and matched nothing.
func cpakStateMasks(directories []string, home, source, target string) []maskedCpakPath {
	masks := []maskedCpakPath{}
	for _, directory := range directories {
		if container, ok := grantedContainerPath(source, target, directory); ok {
			masks = append(masks, maskedCpakPath{host: directory, container: container, directory: true})
		}
	}
	if home == "" {
		return masks
	}
	binaries, err := filepath.Glob(filepath.Join(home, ".local", "bin", "cpak*"))
	if err != nil {
		return masks
	}
	for _, binary := range binaries {
		if container, ok := grantedContainerPath(source, target, binary); ok {
			masks = append(masks, maskedCpakPath{host: binary, container: container})
		}
	}
	return masks
}

func grantedContainerPath(source, target, path string) (string, bool) {
	if !pathWithin(source, path) {
		return "", false
	}
	relative, err := filepath.Rel(source, path)
	if err != nil {
		return "", false
	}
	return filepath.Join(target, relative), true
}

// maskCpakState hides cpak's own state from a grant that happens to contain it.
// The store, the policies, the exported launchers and the cpak binaries are not
// application data: reaching them from inside one application means reaching
// every other one, and the process that would have checked them.
//
// A read-only grant is masked only where the state already exists, because it
// is bound from a tree that refuses the directory a mask would otherwise have
// to create, and a read-only mount cannot be used to plant state either.
func (c *SpawnCmd) maskCpakState(rootFs, source, target string, readOnly bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	empty := ""
	for _, masked := range cpakStateMasks(c.maskedStateDirectories(), home, source, target) {
		if readOnly {
			if _, statErr := os.Stat(masked.host); statErr != nil {
				continue
			}
		}
		c.spawnVerbose("(filesystem) Masking: ", masked.container)
		if masked.directory {
			destination, prepareErr := prepareRootfsDirectory(rootFs, masked.container)
			if prepareErr != nil {
				return fmt.Errorf("prepare masked directory %s: %w", masked.container, prepareErr)
			}
			if err := tools.MountTmpfsPrepared(destination); err != nil {
				return fmt.Errorf("mask %s: %w", masked.container, err)
			}
			continue
		}
		// The empty file is written once per call and only when it is absent,
		// because the mask now runs from several call sites and the file it
		// binds over is mode 0444.
		if empty == "" {
			empty = filepath.Join(c.StateDir, "masked")
			if _, statErr := os.Stat(empty); statErr != nil {
				if err := os.WriteFile(empty, nil, 0444); err != nil {
					return fmt.Errorf("prepare mask source: %w", err)
				}
			}
		}
		destination, prepareErr := prepareRootfsFile(rootFs, masked.container)
		if prepareErr != nil {
			return fmt.Errorf("prepare masked file %s: %w", masked.container, prepareErr)
		}
		if err := tools.MountFileBindPrepared(empty, destination, true, true); err != nil {
			return fmt.Errorf("mask %s: %w", masked.container, err)
		}
	}
	return nil
}

func pathWithin(parent, candidate string) bool {
	if parent == candidate {
		return true
	}
	return strings.HasPrefix(candidate, strings.TrimSuffix(parent, string(os.PathSeparator))+string(os.PathSeparator))
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
	ptsRoot, err := prepareRootfsDirectory(rootFs, "/dev/pts")
	if err != nil {
		return nil, fmt.Errorf("mkdir:/dev/pts: an error occurred while spawning the namespace: %s", err)
	}
	if err := tools.MountDevptsPrepared(ptsRoot); err != nil {
		return nil, fmt.Errorf("mount:/dev/pts: an error occurred while spawning the namespace: %s", err)
	}
	if err := os.Symlink("pts/ptmx", filepath.Join(deviceRoot, "ptmx")); err != nil {
		return nil, fmt.Errorf("link:/dev/ptmx: an error occurred while spawning the namespace: %s", err)
	}
	for _, device := range []string{"full", "null", "zero", "random", "urandom", "tty"} {
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

func (c *SpawnCmd) injectConfigurationFiles(rootFs string, includeNvidia bool) ([]sandbox.PathGrant, bool, error) {
	grants := []sandbox.PathGrant{}
	var err error
	nvidiaMounts := []cpak.NvidiaMount{}
	if includeNvidia {
		nvidiaMounts, err = cpak.GetNvidiaMounts(rootFs)
		if err != nil {
			return nil, false, fmt.Errorf("an error occurred while spawning the namespace: %s", err)
		}
	}

	files := []string{
		"/etc/resolv.conf",
		"/etc/hosts",
		"/etc/passwd",
		"/etc/group",
		"/etc/localtime",
		"/etc/timezone",
	}

	for _, conf := range files {
		content, readErr := os.ReadFile(conf)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, false, fmt.Errorf("read:%s: an error occurred while spawning the namespace: %s", conf, readErr)
		}
		destination, prepareErr := prepareRootfsConfigurationFile(rootFs, conf)
		if prepareErr != nil {
			return nil, false, fmt.Errorf("prepare:%s: an error occurred while spawning the namespace: %s", conf, prepareErr)
		}
		c.spawnVerbose("Writing: ", conf)
		if err = os.WriteFile(destination, content, 0644); err != nil {
			return nil, false, fmt.Errorf("write:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		if err = os.Chmod(destination, 0644); err != nil {
			return nil, false, fmt.Errorf("chmod:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		if err = tools.MountBindReadOnlyPrepared(destination, destination, true); err != nil {
			return nil, false, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", conf, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: conf, ReadOnly: true})
	}

	for _, mount := range nvidiaMounts {
		c.spawnVerbose("Mounting: ", mount.Source, " as ", mount.Destination)
		destination, prepareErr := prepareRootfsMountTarget(rootFs, mount.Destination, mount.Source)
		if prepareErr != nil {
			return nil, false, fmt.Errorf("prepare:%s: an error occurred while spawning the namespace: %s", mount.Destination, prepareErr)
		}
		if mount.RewriteLibraryPath {
			if err = writeNvidiaLoaderConfiguration(mount.Source, destination); err != nil {
				return nil, false, err
			}
			if err = tools.MountBindReadOnlyPrepared(destination, destination, true); err != nil {
				return nil, false, fmt.Errorf("restrict:%s: an error occurred while spawning the namespace: %s", mount.Destination, err)
			}
		} else if err = tools.MountBindReadOnlyPrepared(mount.Source, destination, false); err != nil {
			return nil, false, fmt.Errorf("mount:%s:%s: an error occurred while spawning the namespace: %s", mount.Source, mount.Destination, err)
		}
		grants = append(grants, sandbox.PathGrant{Path: mount.Destination, ReadOnly: true})
	}

	if len(nvidiaMounts) > 0 {
		_, prepareErr := prepareRootfsDirectory(rootFs, "/etc/ld.so.conf.d")
		if prepareErr != nil {
			return nil, false, fmt.Errorf("mkdir:/etc/ld.so.conf.d: an error occurred while spawning the namespace: %s", prepareErr)
		}
		ldConfig := strings.Join(cpak.NvidiaLibraryDirs(), "\n") + "\n"
		ldConfigPath, prepareErr := prepareRootfsFile(rootFs, "/etc/ld.so.conf.d/cpak-nvidia.conf")
		if prepareErr != nil {
			return nil, false, fmt.Errorf("prepare:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", prepareErr)
		}
		if err = os.WriteFile(ldConfigPath, []byte(ldConfig), 0644); err != nil {
			return nil, false, fmt.Errorf("write:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
		}
		if err = os.Chmod(ldConfigPath, 0644); err != nil {
			return nil, false, fmt.Errorf("chmod:/etc/ld.so.conf.d/cpak-nvidia.conf: an error occurred while spawning the namespace: %s", err)
		}
	}

	return grants, len(nvidiaMounts) > 0, nil
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

func (c *SpawnCmd) setupDesktopRuntime(rootFs, source string) (sandbox.PathGrant, error) {
	if !filepath.IsAbs(source) {
		return sandbox.PathGrant{}, errors.New("desktop runtime path must be absolute")
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return sandbox.PathGrant{}, errors.New("desktop runtime path must be a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return sandbox.PathGrant{}, errors.New("desktop runtime path has an unexpected owner")
	}
	destination, err := prepareRootfsMountTarget(rootFs, desktopRuntimeTarget, source)
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("prepare desktop runtime: %w", err)
	}
	if err := tools.MountBindPrepared(source, destination); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("mount desktop runtime: %w", err)
	}
	return sandbox.PathGrant{Path: desktopRuntimeTarget}, nil
}

func (c *SpawnCmd) installRuntimePackages(packages, installers, destinations []string) error {
	if len(packages) == 0 {
		return fmt.Errorf("no runtime packages specified")
	}
	if len(installers) == 0 {
		installers = make([]string, len(packages))
		for i := range installers {
			installers[i] = "dpkg"
		}
	}
	if len(packages) != len(installers) {
		return fmt.Errorf("runtime package and installer counts differ")
	}
	if len(destinations) == 0 {
		destinations = make([]string, len(packages))
	}
	if len(packages) != len(destinations) {
		return fmt.Errorf("runtime package and destination counts differ")
	}

	for first := 0; first < len(packages); {
		installer := installers[first]
		last := first + 1
		for last < len(packages) && installers[last] == installer {
			last++
		}
		batch := packages[first:last]
		switch installer {
		case "tar":
			for _, archive := range batch {
				if err := installRuntimeArchive("/", archive); err != nil {
					return fmt.Errorf("archive failed to install runtime source %s: %w", archive, err)
				}
			}
		case "dpkg":
			if err := runRuntimeInstaller(runtimePackageCommand(batch)); err != nil {
				return fmt.Errorf("dpkg failed to install runtime packages: %w", err)
			}
		case "deb-extract":
			for _, archive := range batch {
				if err := runRuntimeInstaller(runtimeDebExtractCommand(archive)); err != nil {
					return fmt.Errorf("dpkg-deb failed to extract runtime source %s: %w", archive, err)
				}
			}
		case "rpm":
			if err := runRuntimeInstaller(runtimeRPMPackageCommand(batch)); err != nil {
				return fmt.Errorf("rpm failed to install runtime packages: %w", err)
			}
		case "file":
			for index, artifact := range batch {
				destination := destinations[first+index]
				if err := installRuntimeFile("/", artifact, destination); err != nil {
					return fmt.Errorf("file failed to install runtime source %s: %w", artifact, err)
				}
			}
		default:
			return fmt.Errorf("unsupported runtime installer %q", installer)
		}
		first = last
	}
	return nil
}

func installRuntimeFile(root, artifact, destination string) error {
	if filepath.Clean(destination) != destination || !strings.HasPrefix(destination, "/opt/") {
		return fmt.Errorf("invalid runtime file destination %q", destination)
	}
	source, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer source.Close()
	rootfs, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootfs.Close()
	target := strings.TrimPrefix(destination, "/")
	if err = rootfs.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if info, statErr := rootfs.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime file replaces a symbolic link: %s", destination)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	output, err := rootfs.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, source)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func runRuntimeInstaller(cmd *exec.Cmd) error {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runtimePackageCommand(packages []string) *exec.Cmd {
	args := append([]string{"--install"}, packages...)
	cmd := exec.Command("/usr/bin/dpkg", args...)
	cmd.Env = runtimeInstallerEnvironment()
	return cmd
}

func runtimeDebExtractCommand(archive string) *exec.Cmd {
	cmd := exec.Command("/usr/bin/dpkg-deb", "--extract", archive, "/")
	cmd.Env = runtimeInstallerEnvironment()
	return cmd
}

func runtimeRPMPackageCommand(packages []string) *exec.Cmd {
	args := append([]string{"--install", "--replacepkgs"}, packages...)
	cmd := exec.Command("/usr/bin/rpm", args...)
	cmd.Env = runtimeInstallerEnvironment()
	return cmd
}

func runtimeInstallerEnvironment() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}
}

func installRuntimeArchive(root, archivePath string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	buffered := bufio.NewReader(file)
	reader := io.Reader(buffered)
	var compressed *gzip.Reader
	magic, _ := buffered.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err = gzip.NewReader(buffered)
		if err != nil {
			return err
		}
		defer compressed.Close()
		reader = compressed
	}

	rootfs, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootfs.Close()

	archive := tar.NewReader(reader)
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			return nil
		}
		if nextErr != nil {
			return nextErr
		}
		name, err := runtimeArchiveName(header.Name)
		if err != nil {
			return err
		}
		if err = installRuntimeArchiveEntry(rootfs, name, header, archive); err != nil {
			return err
		}
	}
}

func runtimeArchiveName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || path.IsAbs(name) {
		return "", fmt.Errorf("invalid archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid archive path %q", name)
	}
	return clean, nil
}

func installRuntimeArchiveEntry(root *os.Root, name string, header *tar.Header, reader io.Reader) error {
	if name == "." {
		if header.Typeflag == tar.TypeDir {
			return nil
		}
		return fmt.Errorf("invalid archive path %q", header.Name)
	}
	target := filepath.FromSlash(name)
	if err := root.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	mode := os.FileMode(header.Mode) & os.ModePerm
	switch header.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(target, mode); err != nil {
			return err
		}
		return root.Chmod(target, mode)
	case tar.TypeReg, tar.TypeRegA:
		if info, err := root.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive file replaces a symbolic link: %s", name)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		file, err := root.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return root.Chmod(target, mode)
	case tar.TypeSymlink:
		if _, err := runtimeArchiveName(path.Join(path.Dir(name), header.Linkname)); err != nil || path.IsAbs(header.Linkname) {
			return fmt.Errorf("invalid archive symbolic link %q", header.Linkname)
		}
		if _, err := root.Lstat(target); !os.IsNotExist(err) {
			if err == nil {
				return fmt.Errorf("archive symbolic link replaces an existing path: %s", name)
			}
			return err
		}
		return root.Symlink(header.Linkname, target)
	case tar.TypeLink:
		linkTarget, err := runtimeArchiveName(header.Linkname)
		if err != nil {
			return fmt.Errorf("invalid archive hard link %q", header.Linkname)
		}
		info, err := root.Lstat(filepath.FromSlash(linkTarget))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive hard link target is not a regular file: %s", header.Linkname)
		}
		return root.Link(filepath.FromSlash(linkTarget), target)
	case tar.TypeXHeader, tar.TypeXGlobalHeader:
		return nil
	default:
		return fmt.Errorf("archive contains unsupported entry type %d at %s", header.Typeflag, name)
	}
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

func (c *SpawnCmd) createGrantListener() (net.Listener, error) {
	if c.GrantSocket == "" {
		return nil, errors.New("grant socket is required")
	}
	if err := os.MkdirAll(filepath.Dir(c.GrantSocket), 0700); err != nil {
		return nil, fmt.Errorf("create grant socket directory: %w", err)
	}
	if err := os.Remove(c.GrantSocket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale grant socket: %w", err)
	}
	listener, err := net.Listen("unixpacket", c.GrantSocket)
	if err != nil {
		return nil, fmt.Errorf("listen on grant socket: %w", err)
	}
	if err = os.Chmod(c.GrantSocket, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("restrict grant socket: %w", err)
	}
	return listener, nil
}

func (c *SpawnCmd) setupGrantRoot(rootFs string) (sandbox.PathGrant, error) {
	target, err := prepareRootfsDirectory(rootFs, filegrant.GuestRoot)
	if err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("prepare file grant root: %w", err)
	}
	if err = os.Chmod(target, 0555); err != nil {
		return sandbox.PathGrant{}, fmt.Errorf("restrict file grant root: %w", err)
	}
	return sandbox.PathGrant{Path: filegrant.GuestRoot}, nil
}

func (c *SpawnCmd) serveInit(listener *net.UnixListener, grantListener net.Listener, grantMounts *grantMountWorker, envVars []string, grants []sandbox.PathGrant, idleTimeout time.Duration, refreshDynamicLinker bool) error {
	if refreshDynamicLinker {
		c.spawnVerbose("Reconfiguring dynamic linker run-time bindings")
	}
	if _, err := os.Stat("/sbin/ldconfig"); refreshDynamicLinker && err == nil {
		l := exec.Command("/sbin/ldconfig")
		if err = l.Run(); err != nil {
			return fmt.Errorf("ldconfig: an error occurred while spawning the namespace: %s", err)
		}
	}
	for _, env := range envVars {
		if strings.HasPrefix(env, "CPAK_") {
			c.spawnVerbose("CPAK env var found: ", env)
		}
	}
	if err := c.signalReady(); err != nil {
		return err
	}
	go c.serveGrantMounts(grantListener, grantMounts)
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
			c.handleRuntimeConnection(connection, envVars, grants)
		}()
	}
}

func (c *SpawnCmd) serveGrantMounts(listener net.Listener, grantMounts *grantMountWorker) {
	for {
		accepted, err := listener.Accept()
		if err != nil {
			return
		}
		connection, ok := accepted.(*net.UnixConn)
		if !ok {
			accepted.Close()
			continue
		}
		go c.handleGrantMount(connection, grantMounts)
	}
}

func (c *SpawnCmd) handleGrantMount(connection *net.UnixConn, grantMounts *grantMountWorker) {
	defer connection.Close()
	request, sources, err := grantproto.Receive(connection)
	if err == nil {
		defer sources.Close()
		err = mountFileGrant(request.Grant, sources, grantMounts)
	}
	response := grantproto.Response{Target: request.Grant.Target}
	if err != nil {
		response = grantproto.Response{Error: err.Error()}
	}
	_ = grantproto.Reply(connection, response)
}

func mountFileGrant(grant filegrant.Grant, sources grantproto.Sources, grantMounts *grantMountWorker) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	if sources.Selected == nil {
		return errors.New("file grant source descriptor is required")
	}
	info, err := sources.Selected.Stat()
	if err != nil {
		return fmt.Errorf("inspect file grant descriptor: %w", err)
	}
	if grant.Kind == filegrant.KindDirectory && !info.IsDir() || grant.Kind == filegrant.KindFile && !info.Mode().IsRegular() {
		return errors.New("file grant descriptor type does not match")
	}
	parent := filepath.Dir(grant.MountTarget)
	if err = os.MkdirAll(parent, 0555); err != nil {
		return fmt.Errorf("create file grant target: %w", err)
	}
	if grant.Kind == filegrant.KindDirectory {
		if err = os.MkdirAll(grant.MountTarget, 0555); err != nil {
			return fmt.Errorf("create directory grant target: %w", err)
		}
	} else {
		file, createErr := os.OpenFile(grant.MountTarget, os.O_CREATE|os.O_RDONLY, 0400)
		if createErr != nil {
			return fmt.Errorf("create file grant target: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
	}
	readOnly := grant.Access == filegrant.AccessReadOnly
	tree, err := grantMounts.Clone(grant, sources)
	if err != nil {
		return fmt.Errorf("prepare file grant mount: %w", err)
	}
	defer tree.Close()
	if grant.Kind == filegrant.KindFile {
		err = mountExactFileGrant(grant, sources, tree, readOnly)
	} else {
		err = tools.AttachDescriptorMountPrepared(int(tree.Fd()), grant.MountTarget)
	}
	if err != nil {
		return fmt.Errorf("mount file grant: %w", err)
	}
	return nil
}

func mountExactFileGrant(grant filegrant.Grant, sources grantproto.Sources, tree *os.File, readOnly bool) error {
	if sources.Mount == nil {
		return errors.New("file grant parent descriptor is required")
	}
	staging, err := os.MkdirTemp("/run/cpak", ".grant-")
	if err != nil {
		return fmt.Errorf("create file grant staging directory: %w", err)
	}
	defer os.Remove(staging)
	if err = tools.AttachDescriptorMountPrepared(int(tree.Fd()), staging); err != nil {
		return fmt.Errorf("mount file grant parent: %w", err)
	}
	defer syscall.Unmount(staging, syscall.MNT_DETACH)
	selected := filepath.Join(staging, filepath.Base(grant.Source))
	selectedInfo, err := os.Stat(selected)
	if err != nil {
		return fmt.Errorf("inspect staged file grant: %w", err)
	}
	sourceInfo, err := sources.Selected.Stat()
	if err != nil {
		return fmt.Errorf("inspect selected file grant: %w", err)
	}
	if !os.SameFile(selectedInfo, sourceInfo) {
		return errors.New("file grant source changed while mounting")
	}
	return tools.MountFileBindPrepared(selected, grant.MountTarget, readOnly, true)
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

func (c *SpawnCmd) handleRuntimeConnection(connection *net.UnixConn, baseEnv []string, grants []sandbox.PathGrant) {
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
	if c.AllowPtrace {
		args = append(args, "--allow-ptrace")
	}
	args = append(args, landlockArguments(grants)...)
	args = append(args, "--")
	args = append(args, request.Args...)
	command := exec.Command(cpakInContainerPath, args...)
	command.Env = append(append([]string{}, baseEnv...), request.Env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Whether a nested command may run as root is a property of the container,
	// decided when it was created. Taking it from the request would let anyone
	// who can reach the socket ask for it.
	// The application always gets a user namespace of its own, and asRoot only
	// decides which identity it holds inside it.
	//
	// It used to decide whether there was one at all, and that was the whole of
	// what separated the application from the container's init. The file grant
	// worker runs on a thread that unshared its mount namespace before the
	// pivot, so it keeps a root that is still the host's for the life of the
	// container, reachable as /proc/1/task/<tid>/root. Sharing a user namespace
	// and credentials with init made that path readable: measured, an ordinary
	// application gets EPERM there and an asRoot one enumerated the host's root
	// directory. A nested namespace has no capability in its parent, so the
	// traversal is refused whichever identity the application was given.
	command.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER
	command.SysProcAttr.GidMappingsEnableSetgroups = false
	if c.AllowRoot {
		command.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}}
		command.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: 0, Size: 1}}
	} else {
		command.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 1000, HostID: 0, Size: 1}}
		command.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 1000, HostID: 0, Size: 1}}
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

func landlockArguments(grants []sandbox.PathGrant) []string {
	args := make([]string, 0, len(grants)*2)
	for _, grant := range grants {
		flag := "--landlock-read-write"
		if grant.WriteFiles {
			flag = "--landlock-write-files"
		} else if grant.ReadOnly {
			flag = "--landlock-read-only"
		}
		args = append(args, flag, grant.Path)
	}
	return args
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
	if err := os.Chmod(shimFilePath, 0755); err != nil {
		return fmt.Errorf("chmod system broker shim: %w", err)
	}
	for _, name := range shims {
		if name != "notify-send" && name != "xdg-open" && name != "gio" && name != "cpak-launch-app" && name != "cpak-file-picker" && name != "podman" && name != "docker" {
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

func (c *SpawnCmd) installOpenURIHandler(rootFs string) error {
	path, err := prepareRootfsFile(rootFs, openURIHandlerDesktopPath)
	if err != nil {
		return fmt.Errorf("prepare open URI desktop entry: %w", err)
	}
	if err := os.WriteFile(path, []byte(openURIHandlerDesktopEntry), 0644); err != nil {
		return fmt.Errorf("write open URI desktop entry: %w", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		return fmt.Errorf("chmod open URI desktop entry: %w", err)
	}
	defaults, err := prepareRootfsFile(rootFs, openURIHandlerDefaultsPath)
	if err != nil {
		return fmt.Errorf("prepare open URI defaults: %w", err)
	}
	if err := os.WriteFile(defaults, []byte(openURIHandlerDefaults), 0644); err != nil {
		return fmt.Errorf("write open URI defaults: %w", err)
	}
	if err := os.Chmod(defaults, 0644); err != nil {
		return fmt.Errorf("chmod open URI defaults: %w", err)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
