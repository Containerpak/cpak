/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

// PrepareContainer dispatches the creation of a new container for the given
// application. If a container for the given application already exists in
// the store, it checks if it is running and, if not, it cleans it up and
// creates a new one, otherwise it attaches to it.
//
// Note: in cpak, the container's lifecycle is based on the process lifecycle,
// so if the process dies, the container cannot be attached to anymore. This
// is why we need to check if the container is running before attaching to it.
// There are no plans to change this behaviour since cpak is meant for running
// applications that never store any data on its directories, developers should
// use the user's home directory for that or expose other system directories
// where data can be stored.
func (c *Cpak) PrepareContainer(app types.Application, override types.Override) (container types.Container, err error) {
	return c.prepareContainer(app, asLaunched(override), app.CpakId, "", nil, nil)
}

func (c *Cpak) PrepareContainerInstance(app types.Application, override types.Override, instance string) (types.Container, error) {
	scope := ApplicationScope(app.CpakId, instance)
	return c.prepareContainer(app, asLaunched(override), scope, instance, nil, nil)
}

func ApplicationScope(applicationCpakId, instance string) string {
	if instance == "" {
		return applicationCpakId
	}
	return applicationCpakId + ":instance:" + instance
}

// PrepareNestedContainer starts a container for an application another package
// asked to run. The override is that package's ceiling, not a policy of its
// own: what the application is recognised by stays the policy it was enrolled
// with, and the ceiling only narrows what the container is built from.
func (c *Cpak) PrepareNestedContainer(app types.Application, override types.Override) (types.Container, error) {
	return c.prepareNestedContainer(app, narrowedTo(resolvedOverride(app), override))
}

func (c *Cpak) prepareNestedContainer(app types.Application, policy launchPolicy) (types.Container, error) {
	scope := app.CpakId + ":nested:" + uuid.NewString()
	return c.prepareContainer(app, policy, scope, "", nil, nil)
}

type persistentContainerState struct {
	scope         string
	upperDir      string
	workDir       string
	dataID        string
	refreshPolicy func(launchPolicy) (launchPolicy, error)
}

// prepareContainer answers to the gate with the policy the application was
// enrolled with and builds the container from the policy it actually runs
// under, which is the same one unless something narrowed this launch.
func (c *Cpak) prepareContainer(app types.Application, policy launchPolicy, scope, instance string, store *Store, persistent *persistentContainerState) (container types.Container, err error) {
	unlock, err := c.lockContainerScope(scope)
	if err != nil {
		return types.Container{}, err
	}
	defer unlock()
	if persistent != nil && persistent.refreshPolicy != nil {
		policy, err = persistent.refreshPolicy(policy)
		if err != nil {
			return types.Container{}, err
		}
	}
	override := policy.effective

	if store == nil {
		store, err = NewStore(c.Options.StorePath)
		if err != nil {
			return
		}
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	addons, err := c.resolveEnabledAddonsFromStore(app, store)
	if err != nil {
		return types.Container{}, err
	}
	components, err := c.resolveLayerDependenciesFromStore(app, store)
	if err != nil {
		return types.Container{}, err
	}
	// The gate answers before anything of the application is mounted, and on
	// the reuse path too: a container that is attached to is a launch as much
	// as a container that is created.
	identity, err := c.gateSessionLaunch(app, policy.enrolled, sessionIDOfInstance(instance), components, addons)
	if err != nil {
		return types.Container{}, err
	}
	policyHash, err := containerLaunchPolicyHash(containerRuntimeVersion(instance), identity.LaunchRoot, override, components, addons)
	if err != nil {
		return types.Container{}, err
	}
	// Check if a container already exists for the given application
	scopedApp := app
	scopedApp.CpakId = scope
	containers, err := store.GetApplicationContainers(scopedApp)
	if err != nil {
		return
	}

	config := &oci.ConfigFile{}
	err = json.Unmarshal([]byte(app.Config), config)
	if err != nil {
		return
	}

	// If a container already exists, check if it is running
	if len(containers) > 0 {
		container = containers[0]
		logger.Println("Container found:", container.CpakId)

		// If the container is not running, we clean it up and create a new one
		// by escaping the if statement
		if container.PolicyHash != policyHash || !containerProcessRunning(container) || !containerDesktopBusAlive(container) || !containerBluetoothBusAlive(container) || !containerX11BridgeAlive(container) || !c.containerLayerMountAlive(container) {
			logger.Println("Container cannot be reused, cleaning it up:", container.CpakId)
			if containerProcessRunning(container) {
				terminateContainerProcess(container)
			}
			if err = store.Close(); err != nil {
				return
			}
			err = c.CleanupContainer(container)
			if err != nil {
				return
			}
			store, err = NewStore(c.Options.StorePath)
			if err != nil {
				return
			}
		} else {
			logger.Println("Container already running, attaching to it:", container.CpakId)
			return
		}
	}

	// If no container exists, create a new one and store it
	// Note: the container's pid is not set here, it will be set when the
	// container is started by the StartContainer function
	newContainerCpakId := uuid.New().String()
	statePath, err := c.GetInStoreDirMkdir("states", newContainerCpakId)
	if err != nil {
		return
	}

	_, err = c.GetInStoreDirMkdir("containers", newContainerCpakId, "rootfs")
	if err != nil {
		os.RemoveAll(statePath)
		return
	}

	container = types.Container{
		CpakId:            newContainerCpakId,
		ApplicationCpakId: scope,
		Instance:          instance,
		StatePath:         statePath,
		LogPath:           filepath.Join(statePath, "application.log"),
		PolicyHash:        policyHash,
		CreateTimestamp:   time.Now(),
	}
	if persistent != nil {
		container.WritableLayerPath = persistent.upperDir
		container.WritableWorkPath = persistent.workDir
		container.DataID = persistent.dataID
	}

	container.ExecSocketPath = filepath.Join(container.StatePath, "exec.sock")
	container.GrantSocketPath = filepath.Join(container.StatePath, "grant.sock")
	layers := composedLayers(app, components, addons)
	container.FVSLayerMountId, container.FVSLayerMountPath, container.FVSManagerSocketPath, err = c.prepareLayerMount(container.StatePath, layers)
	if err != nil {
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, err
	}
	mountOwned := true
	defer func() {
		if err != nil && mountOwned {
			_ = c.releaseFVSMount(container.FVSLayerMountId, container.FVSManagerSocketPath)
		}
	}()
	if applicationHasNestedDependencies(app) {
		// This capability is useful only to an application that can ask cpak to
		// run one of its declared dependencies. Containers without one get
		// neither a token nor the host service socket.
		container.NestedToken, err = newNestedToken()
		if err != nil {
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
	}
	if len(systemBrokerShims(override)) > 0 {
		container.SystemBrokerSocketPath, container.SystemBrokerTokenPath, err = createSystemBrokerRuntime(container.StatePath)
		if err != nil {
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		if err = writeSystemBrokerToken(container.SystemBrokerTokenPath); err != nil {
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		desktopRuntime := ""
		if override.HostApplications {
			_, _, err = prepareHostApplicationCatalog(container.StatePath)
			if err == nil {
				desktopRuntime, err = createDesktopRuntime(container.StatePath)
			}
			if err != nil {
				cleanupSystemBrokerRuntime(container)
				os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
				os.RemoveAll(container.StatePath)
				return types.Container{}, err
			}
		}
		container.SystemBrokerPolicyPath, err = c.registerSystemBrokerPolicy(container.SystemBrokerTokenPath, desktopRuntime, app.CpakId, app.Name, app.Origin, override, container.StatePath, container.GrantSocketPath)
		if err != nil {
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, fmt.Errorf("failed to register system broker policy: %w", err)
		}
	} else {
		container.SystemBrokerSocketPath = ""
		container.SystemBrokerTokenPath = ""
	}
	if override.FilePicker.Enabled() || override.SessionBus.Enabled() {
		container.DesktopBusSocketPath = filepath.Join(container.StatePath, "desktop-bus.sock")
		container.DesktopBusProxyPid, err = startDesktopBusProxy(container, override)
		if err != nil {
			cleanupDesktopBusProxy(container)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		container.DesktopBusProxyStartTime, err = processStartTime(container.DesktopBusProxyPid)
		if err != nil {
			_ = syscall.Kill(container.DesktopBusProxyPid, syscall.SIGTERM)
			cleanupDesktopBusProxy(container)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, fmt.Errorf("identify desktop bus proxy: %w", err)
		}
	}
	if bluetoothProxyRequested(override) {
		container.BluetoothBusSocketPath = filepath.Join(container.StatePath, "bluetooth-bus.sock")
		container.BluetoothBusProxyPid, err = startBluetoothBusProxy(container)
		if err != nil {
			cleanupBluetoothBusProxy(container)
			cleanupDesktopBusProxy(container)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		container.BluetoothBusProxyStartTime, err = processStartTime(container.BluetoothBusProxyPid)
		if err != nil {
			_ = syscall.Kill(container.BluetoothBusProxyPid, syscall.SIGTERM)
			cleanupBluetoothBusProxy(container)
			cleanupDesktopBusProxy(container)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, fmt.Errorf("identify Bluetooth bus proxy: %w", err)
		}
	}
	if override.DisplayX11 {
		container, err = startX11Bridge(container)
		if err != nil {
			cleanupX11Bridge(container)
			cleanupBluetoothBusProxy(container)
			cleanupDesktopBusProxy(container)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
	}

	err = store.NewContainer(container)
	if err != nil {
		cleanupX11Bridge(container)
		cleanupBluetoothBusProxy(container)
		cleanupDesktopBusProxy(container)
		cleanupSystemBrokerRuntime(container)
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, err
	}
	logger.Println("Container created:", container.CpakId)
	if err = store.Close(); err != nil {
		cleanupX11Bridge(container)
		cleanupBluetoothBusProxy(container)
		cleanupDesktopBusProxy(container)
		cleanupSystemBrokerRuntime(container)
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, err
	}
	store = nil

	_, container.Pid, container.CgroupPath, err = c.StartContainer(container, app, components, addons, config, override)
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}
	container.ProcessStartTime, err = processStartTime(container.Pid)
	if err != nil {
		terminateContainerProcess(container)
		c.CleanupContainer(container)
		return types.Container{}, fmt.Errorf("identify container process: %w", err)
	}
	if override.FilePicker.Enabled() {
		err = c.mountPersistentFileGrants(app.Origin, container)
	}
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}
	store, err = NewStore(c.Options.StorePath)
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}
	if err = store.SetContainerRuntime(container.CpakId, container.Pid, container.ProcessStartTime, container.CgroupPath); err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}

	mountOwned = false
	logger.Println("Container prepared:", container.CpakId)
	return
}

func (c *Cpak) lockContainerScope(scope string) (func(), error) {
	directory, err := c.GetInStoreDirMkdir("locks", "containers")
	if err != nil {
		return nil, fmt.Errorf("create container lock directory: %w", err)
	}
	digest := sha256.Sum256([]byte(scope))
	path := filepath.Join(directory, hex.EncodeToString(digest[:])+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open container lock: %w", err)
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("lock container scope: %w", err)
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

const containerRuntimePolicyVersion = 6
const loginSessionRuntimePolicyVersion = 7

func containerRuntimeVersion(instance string) int {
	if isSessionInstance(instance) {
		return loginSessionRuntimePolicyVersion
	}
	return containerRuntimePolicyVersion
}

const openURIMimeApps = `[Default Applications]
x-scheme-handler/http=cpak-open-uri.desktop;
x-scheme-handler/https=cpak-open-uri.desktop;
x-scheme-handler/mailto=cpak-open-uri.desktop;
`

func containerPolicyHash(override types.Override, components, addons []types.Application) (string, error) {
	return containerPolicyHashVersion(containerRuntimePolicyVersion, override, components, addons)
}

func containerPolicyHashVersion(runtimeVersion int, override types.Override, components, addons []types.Application) (string, error) {
	return containerLaunchPolicyHash(runtimeVersion, "", override, components, addons)
}

// containerLaunchPolicyHash folds the launch root into the key a container is
// reused by, so that a container cannot outlive the state its launch was
// recognised as. A launch that carries no root hashes the way it did before
// there was one, which is what keeps the containers of an unenrolled
// application from being rebuilt for nothing.
func containerLaunchPolicyHash(runtimeVersion int, launchRoot string, override types.Override, components, addons []types.Application) (string, error) {
	policy := struct {
		Runtime    int                   `json:"runtime"`
		LaunchRoot string                `json:"launch_root,omitempty"`
		Override   types.Override        `json:"override"`
		Components []addonPolicyIdentity `json:"components,omitempty"`
		Addons     []addonPolicyIdentity `json:"addons,omitempty"`
	}{
		Runtime:    runtimeVersion,
		LaunchRoot: launchRoot,
		Override:   override,
		Components: addonPolicyIdentities(components),
		Addons:     addonPolicyIdentities(addons),
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode container policy: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", hash[:]), nil
}

// StartContainer starts the container with the given config and image.
// The config is used to set the environment the way the developer wants.
// The container is started by calling our spawn function, which is the
// responsible for setting up the pivot root, mounting the layers and
// replacing itself with the init process inside native Linux namespaces.
func (c *Cpak) StartContainer(container types.Container, app types.Application, components, addons []types.Application, config *oci.ConfigFile, override types.Override) (rootfs string, pid int, cgroupPath string, err error) {
	network, err := resolveUserNetwork(override.Network, override.HostNetwork)
	if err != nil {
		return "", 0, "", err
	}
	if override.OpenURI {
		if err = ensureOpenURIMimeApps(os.Environ()); err != nil {
			return "", 0, "", err
		}
	}
	composed := composedLayers(app, components, addons)
	layers := ""
	for _, layer := range composed {
		layers += layer + "|"
	}

	// the cpakBinary is the path to the cpak binary, it is used to re-execute
	// the cpak with the spawn command to start the container
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	rootfs = c.GetInStoreDir("containers", container.CpakId, "rootfs")
	overrideMounts, _ := GetOverrideMounts(override)
	if container.DesktopBusSocketPath != "" {
		overrideMounts = withoutMount(overrideMounts, hostSessionBusPath())
	}
	if container.BluetoothBusSocketPath != "" {
		overrideMounts = withoutMount(overrideMounts, hostSystemBusPath())
	}
	if container.X11SocketPath != "" {
		overrideMounts = withoutMount(overrideMounts, "/tmp/.X11-unix")
	}
	filesystemArgs := []string{}
	for _, permission := range override.Filesystem {
		encoded, encodeErr := types.EncodeFilesystemPermission(permission)
		if encodeErr != nil {
			return "", 0, "", encodeErr
		}
		filesystemArgs = append(filesystemArgs, "--filesystem", encoded)
	}
	cmds := []string{}
	cmds = append(cmds, "spawn")
	cmds = append(cmds, filesystemArgs...)
	if isVerbose {
		cmds = append(cmds, "--verbose")
	}
	cmds = append(cmds, "--user-uid", strconv.Itoa(os.Getuid()))
	cmds = append(cmds, "--app-id", app.CpakId)
	dataID := container.DataID
	if dataID == "" {
		dataID = app.CpakId
	}
	machineID, err := c.applicationMachineID(dataID)
	if err != nil {
		return "", 0, "", err
	}
	cmds = append(cmds, "--machine-id", machineID)
	cmds = append(cmds, "--container-id", container.CpakId)
	cmds = append(cmds, "--rootfs", rootfs)
	cmds = append(cmds, "--state-dir", container.StatePath)
	if container.WritableLayerPath != "" {
		cmds = append(cmds, "--upper-dir", container.WritableLayerPath)
	}
	if container.WritableWorkPath != "" {
		cmds = append(cmds, "--work-dir", container.WritableWorkPath)
	}
	// The mask has to be told where this installation keeps its state, because
	// spawn runs as its own process and resolves nothing: the paths come from
	// the options cpak loaded, which the environment can move one by one.
	for _, directory := range c.cpakStateDirectories() {
		cmds = append(cmds, "--mask-state", directory)
	}
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--layers-dir", c.GetInStoreDir("layers"))
	if override.AsRoot {
		cmds = append(cmds, "--allow-root")
	}
	cmds = append(cmds, "--lower-dir", container.FVSLayerMountPath)
	cmds = append(cmds, "--ready-fd", "3")
	cmds = append(cmds, "--exec-socket", container.ExecSocketPath)
	grantSocketPath := container.GrantSocketPath
	if grantSocketPath == "" {
		grantSocketPath = filepath.Join(container.StatePath, "grant.sock")
	}
	cmds = append(cmds, "--grant-socket", grantSocketPath)
	if !filesystemIncludesHostHome(override) {
		privateHome, homeErr := c.privateApplicationHome(dataID)
		if homeErr != nil {
			return "", 0, "", homeErr
		}
		cmds = append(cmds, "--private-home", privateHome)
	}
	cmds = append(cmds, "--idle-time", strconv.Itoa(app.IdleTime))
	if override.FsHost {
		cmds = append(cmds, "--mount-host-root")
	}
	if isSessionInstance(container.Instance) {
		cmds = append(cmds, "--login-session")
	}
	if override.DeviceDri || override.DeviceAll {
		cmds = append(cmds, "--nvidia")
	}
	if override.UserNamespaces {
		cmds = append(cmds, "--user-namespaces")
	}
	if applicationPtraceAllowed(override) {
		cmds = append(cmds, "--allow-ptrace")
	}
	if network != nil {
		cmds = append(cmds, "--nameserver", slirpNameserver)
	}

	// Mount the main cpak binary into a known location inside the container
	cpakInContainerPath := "/usr/local/bin/cpak"
	cmds = append(cmds, "--extra-links", cpakBinary+":"+cpakInContainerPath)
	if override.HostApplications {
		if source := hostOSReleaseSource(); source != "" {
			cmds = append(cmds, "--extra-links", source+":"+hostOSReleaseTarget)
		}
	}
	dependencyLinks, err := c.dependencyLinks(app)
	if err != nil {
		return "", 0, "", err
	}
	for _, link := range dependencyLinks {
		cmds = append(cmds, "--extra-links", link)
	}

	if container.SystemBrokerSocketPath != "" {
		cmds = append(cmds, "--env", "CPAK_SYSTEM_BROKER_SOCKET="+systemBrokerSocketTarget)
		cmds = append(cmds, "--env", "CPAK_SYSTEM_BROKER_TOKEN_FILE="+systemBrokerTokenTarget)
		cmds = append(cmds, "--extra-links", container.SystemBrokerSocketPath+":"+systemBrokerSocketTarget)
		cmds = append(cmds, "--extra-links", container.SystemBrokerTokenPath+":"+systemBrokerTokenTarget)
		if override.HostApplications {
			catalogRoot, _ := hostApplicationCatalogPaths(container.StatePath)
			cmds = append(cmds, "--extra-links", catalogRoot+":"+hostApplicationsTarget)
			cmds = append(cmds, "--desktop-runtime", desktopRuntimePath(container.StatePath))
			cmds = append(cmds, "--env", "CPAK_HOST_APPLICATIONS="+filepath.Join(hostApplicationsTarget, "share"))
			cmds = append(cmds, "--env", "CPAK_DESKTOP_RUNTIME="+desktopRuntimeTarget)
		}
	}
	if container.DesktopBusSocketPath != "" {
		guestBusPath := hostSessionBusPath()
		cmds = append(cmds, "--extra-links", container.DesktopBusSocketPath+":"+guestBusPath)
	}
	for _, link := range privateDesktopLinks(container) {
		cmds = append(cmds, "--extra-links", link)
	}
	containerEnv := append([]string{}, config.Config.Env...)
	containerEnv = append(containerEnv, override.Env...)
	containerEnv = applyAddonEnvironment(containerEnv, addons)
	if hostLocaleWins(app) {
		containerEnv = inheritHostLocale(containerEnv, os.Environ())
	}
	containerEnv = inheritHostTimezone(containerEnv)
	containerEnv = inheritHostCursor(containerEnv)
	if override.OpenURI {
		containerEnv = openURIEnvironment(containerEnv)
	}
	if override.HostApplications {
		containerEnv = prependEnvironmentPath(containerEnv, "XDG_DATA_DIRS", filepath.Join(hostApplicationsTarget, "share"), "/usr/local/share:/usr/share")
		if hostOSReleaseSource() != "" {
			containerEnv = append(containerEnv, "CPAK_HOST_OS_RELEASE="+hostOSReleaseTarget)
		}
	}
	if override.SocketWayland {
		containerEnv = append(containerEnv, "WAYLAND_DISPLAY="+waylandDisplay(strconv.Itoa(os.Getuid())))
	}
	if container.DesktopBusSocketPath != "" {
		containerEnv = setEnvironmentValue(containerEnv, "DBUS_SESSION_BUS_ADDRESS", "unix:path="+hostSessionBusPath())
		containerEnv = setEnvironmentValue(containerEnv, "GTK_USE_PORTAL", "1")
	}
	if container.BluetoothBusSocketPath != "" {
		containerEnv = setEnvironmentValue(containerEnv, "DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+hostSystemBusPath())
	}
	if container.X11SocketPath != "" {
		containerEnv = setEnvironmentValue(containerEnv, "DISPLAY", container.X11Display)
		containerEnv = setEnvironmentValue(containerEnv, "XAUTHORITY", x11AuthorityTarget)
	}
	containerEnv = setEnvironmentValue(containerEnv, "PATH", buildContainerPath(containerEnv))
	for _, envVar := range containerEnv {
		cmds = append(cmds, "--env", envVar)
	}
	// After the environment the package asked for, so that the address of the
	// service is cpak's to decide and not something a publisher can name.
	serviceSocketArgs, err := nestedServiceArguments(app, container.NestedToken)
	if err != nil {
		return "", 0, "", err
	}
	cmds = append(cmds, serviceSocketArgs...)

	for _, ovr := range overrideMounts {
		cmds = append(cmds, "--mount-overrides", ovr)
	}

	for _, shim := range systemBrokerShims(override) {
		cmds = append(cmds, "--system-shims", shim)
	}

	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return "", 0, "", fmt.Errorf("create readiness pipe: %w", err)
	}
	defer readyReader.Close()
	defer readyWriter.Close()

	var networkExitReader, networkExitWriter *os.File
	if network != nil {
		networkExitReader, networkExitWriter, err = os.Pipe()
		if err != nil {
			return "", 0, "", fmt.Errorf("create network lifecycle pipe: %w", err)
		}
		defer networkExitReader.Close()
		defer networkExitWriter.Close()
	}

	cmd := nativeNamespaceCommand(cpakBinary, cmds, containerNamespaceOptions(override))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), containerEnv...)
	cmd.Env = append(cmd.Env, "CPAK_CONTAINER_ID="+container.CpakId)
	cmd.ExtraFiles = []*os.File{readyWriter}
	if networkExitWriter != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, networkExitWriter)
	}

	if err = cmd.Start(); err != nil {
		return "", 0, "", fmt.Errorf("start container namespace: %w", err)
	}
	_ = readyWriter.Close()
	if networkExitWriter != nil {
		_ = networkExitWriter.Close()
	}

	var networkReady <-chan error
	var networkExited <-chan error
	if network != nil {
		helperReadyReader, helperReadyWriter, pipeErr := os.Pipe()
		if pipeErr != nil {
			_ = cmd.Process.Kill()
			return "", 0, "", fmt.Errorf("create network readiness pipe: %w", pipeErr)
		}
		helper := network.command(cmd.Process.Pid, helperReadyWriter, networkExitReader)
		helper.Stdout = os.Stdout
		helper.Stderr = os.Stderr
		if err = helper.Start(); err != nil {
			helperReadyReader.Close()
			helperReadyWriter.Close()
			_ = cmd.Process.Kill()
			return "", 0, "", fmt.Errorf("start network helper: %w", err)
		}
		_ = helperReadyWriter.Close()
		_ = networkExitReader.Close()

		readyResult := make(chan error, 1)
		go func() {
			defer helperReadyReader.Close()
			readyResult <- readNetworkReady(helperReadyReader)
		}()
		exitResult := make(chan error, 1)
		go func() {
			waitErr := helper.Wait()
			exitResult <- waitErr
			if syscall.Kill(cmd.Process.Pid, 0) == nil {
				_ = cmd.Process.Kill()
			}
		}()
		networkReady = readyResult
		networkExited = exitResult
	}

	ready := make(chan error, 1)
	go func() {
		buffer := []byte{0}
		_, readErr := io.ReadFull(readyReader, buffer)
		if readErr == nil && buffer[0] != 1 {
			readErr = fmt.Errorf("invalid readiness response")
		}
		ready <- readErr
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	containerReady := false
	networkIsReady := network == nil
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for !containerReady || !networkIsReady {
		select {
		case err = <-ready:
			if err != nil {
				_ = cmd.Process.Kill()
				return "", 0, "", fmt.Errorf("container failed before readiness: %w", err)
			}
			containerReady = true
			ready = nil
		case err = <-networkReady:
			if err != nil {
				_ = cmd.Process.Kill()
				return "", 0, "", fmt.Errorf("network helper failed before readiness: %w", err)
			}
			networkIsReady = true
			networkReady = nil
		case err = <-networkExited:
			if err == nil {
				err = fmt.Errorf("network helper exited before readiness")
			}
			return "", 0, "", err
		case err = <-exited:
			if err == nil {
				err = fmt.Errorf("container init exited before readiness")
			}
			return "", 0, "", err
		case <-timeout.C:
			_ = cmd.Process.Kill()
			return "", 0, "", fmt.Errorf("container readiness timed out")
		}
	}

	pid = cmd.Process.Pid
	cgroupPath, err = applyCgroupLimits(container.CpakId, pid, override)
	if err != nil {
		_ = cmd.Process.Kill()
		return "", 0, "", err
	}
	if err = syscall.Kill(pid, 0); err != nil {
		return "", 0, "", fmt.Errorf("container init is not running: %w", err)
	}
	return
}

func containerNamespaceOptions(override types.Override) namespaceOptions {
	return namespaceOptions{
		IsolateNetwork: !override.HostNetwork,
		ShareProcesses: override.Process,
		IsolateCgroup:  true,
	}
}

func applicationPtraceAllowed(override types.Override) bool {
	return override.UserNamespaces && !override.Process
}

func inheritHostTimezone(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "TZ=") {
			result = append(result, entry)
		}
	}
	return result
}

// StopContainer stops the containers related to the given application.
func (c *Cpak) StopContainer(app types.Application) (err error) {
	return c.StopContainerInstance(app, "")
}

func (c *Cpak) StopContainerInstance(app types.Application, instance string) (err error) {
	if instance != "" {
		app.CpakId = ApplicationScope(app.CpakId, instance)
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		_ = store.Close()
		return
	}
	if err = store.Close(); err != nil {
		return
	}

	for _, container := range containers {
		terminateContainerProcess(container)
		cleanupErr := c.CleanupContainer(container)
		if cleanupErr != nil {
			logger.Printf("Warning: error during container cleanup %s: %v", container.CpakId, cleanupErr)
		}
	}
	return
}

func terminateContainerProcess(container types.Container) {
	pid, ok := verifiedContainerProcess(container)
	if !ok {
		return
	}
	logger.Println("Stopping container process:", pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !sameContainerProcess(container, pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// Stop is a convenient wrapper around the StopContainer function that
// takes the origin and version of the application to stop.
func (c *Cpak) Stop(origin, version, branch, commit, release string) (err error) {
	return c.StopInstance(origin, version, branch, commit, release, "")
}

func (c *Cpak) StopInstance(origin, version, branch, commit, release, instance string) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil {
		_ = store.Close()
		return
	}
	if app.CpakId == "" {
		_ = store.Close()
		return fmt.Errorf("application not found for stopping: %s", origin)
	}
	if err = store.Close(); err != nil {
		return
	}

	err = c.StopContainerInstance(app, instance)
	if err != nil {
		return
	}
	return
}

// ExecInContainer submits a command to the init process already running inside
// the container namespaces.
func (c *Cpak) ExecInContainer(app types.Application, override types.Override, container types.Container, command []string) (err error) {
	pidToEnter, ok := verifiedContainerProcess(container)
	if !ok {
		return fmt.Errorf("container process %s is not the recorded process", container.CpakId)
	}

	addons, err := c.resolveEnabledAddons(app)
	if err != nil {
		return err
	}
	envVars, err := containerEnvironment(app, override, container, addons...)
	if err != nil {
		return err
	}

	execSocketPath := container.ExecSocketPath
	if execSocketPath == "" {
		execSocketPath = filepath.Join(container.StatePath, "exec.sock")
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: execSocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("connect to container process %d: %w", pidToEnter, err)
	}
	defer connection.Close()

	request, err := runtimeproto.EncodeRequest(runtimeproto.Request{
		Args:   command,
		Env:    envVars,
		AsRoot: override.AsRoot,
	})
	if err != nil {
		return err
	}
	writer := runtimeproto.NewWriter(connection)
	if err = writer.Write(runtimeproto.FrameRequest, request); err != nil {
		return fmt.Errorf("send runtime command: %w", err)
	}
	logger.Println("Executing command:", strings.Join(command, " "))

	logFile, logErr := os.OpenFile(container.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logErr != nil {
		return fmt.Errorf("open application log: %w", logErr)
	}
	defer logFile.Close()
	output := io.MultiWriter(os.Stdout, logFile)
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := os.Stdin.Read(buffer)
			if read > 0 {
				if writeErr := writer.Write(runtimeproto.FrameInput, buffer[:read]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				_ = writer.Write(runtimeproto.FrameInputClose, nil)
				return
			}
		}
	}()

	for {
		kind, payload, readErr := runtimeproto.Read(connection)
		if readErr != nil {
			return fmt.Errorf("read runtime response: %w", readErr)
		}
		switch kind {
		case runtimeproto.FrameOutput:
			if _, err = output.Write(payload); err != nil {
				return err
			}
		case runtimeproto.FrameExit:
			code, decodeErr := runtimeproto.DecodeExit(payload)
			if decodeErr != nil {
				return decodeErr
			}
			if code != 0 {
				return &types.ExitError{Code: code}
			}
			return nil
		}
	}
}

func containerEnvironment(app types.Application, override types.Override, container types.Container, addons ...types.Application) ([]string, error) {
	config := &oci.ConfigFile{}
	if err := json.Unmarshal([]byte(app.Config), config); err != nil {
		return nil, fmt.Errorf("decode application config: %w", err)
	}

	envVars := append([]string{}, os.Environ()...)
	envVars = append(envVars, config.Config.Env...)
	envVars = append(envVars, override.Env...)
	envVars = applyAddonEnvironment(envVars, addons)
	envVars = setEnvironmentValue(envVars, "PATH", buildContainerPath(envVars))
	if hostLocaleWins(app) {
		envVars = inheritHostLocale(envVars, os.Environ())
	}
	envVars = inheritHostCursor(envVars)
	if override.SocketAtSpiBus && len(atSpiSocketPaths(fmt.Sprintf("%d", os.Getuid()))) == 0 {
		envVars = setEnvironmentValue(envVars, "NO_AT_BRIDGE", "1")
	}
	if override.OpenURI {
		envVars = openURIEnvironment(envVars)
	}
	if override.HostApplications {
		envVars = prependEnvironmentPath(envVars, "XDG_DATA_DIRS", filepath.Join(hostApplicationsTarget, "share"), "/usr/local/share:/usr/share")
		if hostOSReleaseSource() != "" {
			envVars = append(envVars, "CPAK_HOST_OS_RELEASE="+hostOSReleaseTarget)
		}
	}
	envVars = append(envVars, "CPAK_CONTAINER_ID="+container.CpakId)
	if container.SystemBrokerSocketPath != "" {
		envVars = append(envVars, "CPAK_SYSTEM_BROKER_SOCKET="+systemBrokerSocketTarget)
		envVars = append(envVars, "CPAK_SYSTEM_BROKER_TOKEN_FILE="+systemBrokerTokenTarget)
	}
	if container.DesktopBusSocketPath != "" {
		envVars = setEnvironmentValue(envVars, "DBUS_SESSION_BUS_ADDRESS", "unix:path="+hostSessionBusPath())
		envVars = setEnvironmentValue(envVars, "GTK_USE_PORTAL", "1")
	}
	if container.BluetoothBusSocketPath != "" {
		envVars = setEnvironmentValue(envVars, "DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+hostSystemBusPath())
	}
	if container.X11SocketPath != "" {
		envVars = setEnvironmentValue(envVars, "DISPLAY", container.X11Display)
		envVars = setEnvironmentValue(envVars, "XAUTHORITY", x11AuthorityTarget)
	}
	return envVars, nil
}

const defaultContainerPath = "/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin"

func buildContainerPath(imageEnv []string) string {
	imagePath := ""
	for _, envVar := range imageEnv {
		if strings.HasPrefix(envVar, "PATH=") {
			imagePath = strings.TrimPrefix(envVar, "PATH=")
		}
	}
	if imagePath == "" {
		imagePath = defaultContainerPath
	}
	entries := append([]string{"/usr/local/bin"}, strings.Split(imagePath, ":")...)
	entries = append(entries, strings.Split(defaultContainerPath, ":")...)
	seen := make(map[string]bool, len(entries))
	path := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == "" || strings.Contains(entry, "$") || seen[entry] {
			continue
		}
		seen[entry] = true
		path = append(path, entry)
	}
	return strings.Join(path, ":")
}

func prependEnvironmentPath(environment []string, name, entry, fallback string) []string {
	prefix := name + "="
	value := fallback
	result := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			value = strings.TrimPrefix(variable, prefix)
			continue
		}
		result = append(result, variable)
	}
	return append(result, prefix+entry+":"+value)
}

func openURIEnvironment(environment []string) []string {
	environment = prependEnvironmentPath(environment, "XDG_DATA_DIRS", "/usr/local/share", "/usr/local/share:/usr/share")
	environment = prependEnvironmentPath(environment, "XDG_CONFIG_DIRS", "/usr/local/etc/xdg", "/etc/xdg")
	prefix := "XDG_CURRENT_DESKTOP="
	desktop := ""
	result := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			desktop = strings.TrimPrefix(variable, prefix)
			continue
		}
		result = append(result, variable)
	}
	if desktop == "" {
		desktop = os.Getenv("XDG_CURRENT_DESKTOP")
	}
	for _, name := range strings.Split(desktop, ":") {
		if strings.EqualFold(name, "cpak") {
			return append(result, prefix+desktop)
		}
	}
	if desktop == "" {
		return append(result, prefix+"cpak")
	}
	return append(result, prefix+"cpak:"+desktop)
}

func (c *Cpak) applicationMachineID(applicationID string) (string, error) {
	path, err := c.applicationIdentityPath(applicationID)
	if err != nil {
		return "", fmt.Errorf("create application machine ID: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create application identity directory: %w", err)
	}
	read := func() (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(data))
		if len(value) != 32 {
			return "", errors.New("stored application machine ID is invalid")
		}
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return "", errors.New("stored application machine ID is invalid")
		}
		for _, octet := range decoded {
			if octet != 0 {
				return value, nil
			}
		}
		return "", errors.New("stored application machine ID is invalid")
	}
	if value, err := read(); err == nil {
		return value, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read application machine ID: %w", err)
	}
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate application machine ID: %w", err)
	}
	value := hex.EncodeToString(data)
	if value == strings.Repeat("0", 32) {
		return "", errors.New("generate application machine ID: invalid random value")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		value, err = read()
		if err != nil {
			return "", fmt.Errorf("read concurrent application machine ID: %w", err)
		}
		return value, nil
	}
	if err != nil {
		return "", fmt.Errorf("create application machine ID: %w", err)
	}
	if _, err = file.WriteString(value + "\n"); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write application machine ID: %w", err)
	}
	return value, nil
}

func (c *Cpak) applicationIdentityPath(applicationID string) (string, error) {
	if strings.TrimSpace(applicationID) == "" {
		return "", errors.New("application ID is empty")
	}
	digest := sha256.Sum256([]byte(applicationID))
	return c.GetInStoreDir("identities", hex.EncodeToString(digest[:])+".machine-id"), nil
}

func ensureOpenURIMimeApps(environment []string) error {
	configHome := environmentValue(environment, "XDG_CONFIG_HOME")
	if configHome == "" {
		home := environmentValue(environment, "HOME")
		if home == "" {
			return errors.New("configure open URI integration: HOME is unavailable")
		}
		configHome = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(configHome) || filepath.Clean(configHome) != configHome || strings.ContainsRune(configHome, '\x00') {
		return errors.New("configure open URI integration: XDG config path is invalid")
	}
	if err := os.MkdirAll(configHome, 0700); err != nil {
		return fmt.Errorf("configure open URI integration: create config directory: %w", err)
	}
	path := filepath.Join(configHome, "cpak-mimeapps.list")
	temporary, err := os.CreateTemp(configHome, ".cpak-mimeapps-*")
	if err != nil {
		return fmt.Errorf("configure open URI integration: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0644); err == nil {
		_, err = temporary.WriteString(openURIMimeApps)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("configure open URI integration: write defaults: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("configure open URI integration: replace defaults: %w", err)
	}
	return nil
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	value := ""
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			value = strings.TrimPrefix(variable, prefix)
		}
	}
	return value
}

func setEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, variable := range environment {
		if !strings.HasPrefix(variable, prefix) {
			result = append(result, variable)
		}
	}
	return append(result, prefix+value)
}

func (c *Cpak) dependencyLinks(app types.Application) ([]string, error) {
	if len(app.ParsedDependencies) == 0 {
		return nil, nil
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	links := make([]string, 0)
	names := make(map[string]string)
	for _, dependency := range app.ParsedDependencies {
		if !dependency.IsNested() {
			continue
		}
		child, getErr := store.GetApplicationByCpakId(dependency.Id)
		if getErr != nil {
			return nil, fmt.Errorf("cannot load dependency %s: %w", dependency.Origin, getErr)
		}
		for _, binary := range child.ParsedBinaries {
			name := filepath.Base(binary)
			if owner, exists := names[name]; exists {
				return nil, fmt.Errorf("dependency binary %s is exported by both %s and %s", name, owner, child.Origin)
			}
			names[name] = child.Origin
			source := filepath.Join(append([]string{c.Options.ExportsPath}, append(strings.Split(child.Origin, "/"), name)...)...)
			links = append(links, source+":"+filepath.Join("/usr/local/bin", name))
		}
	}
	return links, nil
}

func containerProcessRunning(container types.Container) bool {
	if _, ok := verifiedContainerProcess(container); !ok {
		return false
	}
	execSocketPath := container.ExecSocketPath
	if execSocketPath == "" {
		execSocketPath = filepath.Join(container.StatePath, "exec.sock")
	}
	return socketIsLive(execSocketPath)
}

func verifiedContainerProcess(container types.Container) (int, bool) {
	if container.Pid <= 0 || !sameContainerProcess(container, container.Pid) {
		return 0, false
	}
	return container.Pid, true
}

func sameContainerProcess(container types.Container, pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	marker := "CPAK_CONTAINER_ID=" + container.CpakId
	environment, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil || !nulSeparatedValue(environment, marker) {
		return false
	}
	if container.ProcessStartTime == 0 {
		return true
	}
	started, err := processStartTime(pid)
	return err == nil && started == container.ProcessStartTime
}

func nulSeparatedValue(data []byte, expected string) bool {
	for _, value := range bytes.Split(data, []byte{0}) {
		if string(value) == expected {
			return true
		}
	}
	return false
}

func processStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return 0, errors.New("incomplete process stat")
	}
	started, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process start time: %w", err)
	}
	return started, nil
}

func sameRecordedProcess(pid int, recordedStart uint64) bool {
	if pid <= 0 || recordedStart == 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	started, err := processStartTime(pid)
	return err == nil && started == recordedStart
}

func containerDesktopBusAlive(container types.Container) bool {
	if container.DesktopBusSocketPath == "" {
		return true
	}
	return sameRecordedProcess(container.DesktopBusProxyPid, container.DesktopBusProxyStartTime) && socketIsLive(container.DesktopBusSocketPath)
}

func containerBluetoothBusAlive(container types.Container) bool {
	if container.BluetoothBusSocketPath == "" {
		return true
	}
	return sameRecordedProcess(container.BluetoothBusProxyPid, container.BluetoothBusProxyStartTime) && socketIsLive(container.BluetoothBusSocketPath)
}

// CleanupContainer removes the container with the given id.
func (c *Cpak) CleanupContainer(container types.Container) (err error) {
	cleanupX11Bridge(container)
	cleanupBluetoothBusProxy(container)
	cleanupDesktopBusProxy(container)
	cleanupSystemBrokerRuntime(container)
	cleanupCgroup(container.CpakId, container.CgroupPath)
	if releaseErr := c.cleanupFVSMount(container.FVSLayerMountId, container.FVSLayerMountPath, container.FVSManagerSocketPath); releaseErr != nil {
		logger.Printf("Cannot release FVS mount %s: %v", container.FVSLayerMountId, releaseErr)
	}
	workPath := container.WritableWorkPath
	if workPath == "" {
		workPath = filepath.Join(container.StatePath, "work")
	}
	_ = os.Chmod(filepath.Join(workPath, "work"), 0o700)
	if container.WritableWorkPath != "" {
		_ = os.RemoveAll(container.WritableWorkPath)
		_ = securePrivateDirectory(container.WritableWorkPath)
	}

	// we don't care about the error here, we just want to make sure that
	// the container filesystem is getting deleted
	os.RemoveAll(container.StatePath)
	os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
	os.RemoveAll(c.GetInStoreDir("states", container.CpakId))

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

	err = store.RemoveContainerByCpakId(container.CpakId)
	if err != nil {
		return
	}
	return
}

func startDesktopBusProxy(container types.Container, override types.Override) (int, error) {
	upstream := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if upstream == "" {
		upstream = "unix:path=" + hostSessionBusPath()
	}
	arguments := []string{
		"desktop-bus-proxy",
		"--socket-path", container.DesktopBusSocketPath,
		"--upstream-address", upstream,
	}
	if override.SessionBus.Enabled() {
		policy, encodeErr := json.Marshal(override.SessionBus)
		if encodeErr != nil {
			return 0, fmt.Errorf("encode desktop bus policy: %w", encodeErr)
		}
		arguments = append(arguments, "--policy", base64.RawURLEncoding.EncodeToString(policy))
	}
	if override.FilePicker.Enabled() {
		arguments = append(arguments,
			"--file-picker",
			"--broker-socket-path", container.SystemBrokerSocketPath,
			"--token-file", container.SystemBrokerTokenPath,
		)
	}
	return startBusProxy(container.LogPath, container.DesktopBusSocketPath, "desktop bus", arguments)
}

func bluetoothProxyRequested(override types.Override) bool {
	return override.Bluetooth || override.SocketBluetooth
}

func startBluetoothBusProxy(container types.Container) (int, error) {
	info, err := os.Stat(hostSystemBusPath())
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return 0, errors.New("Bluetooth requires a host system bus")
	}
	arguments := []string{
		"desktop-bus-proxy",
		"--bluetooth",
		"--socket-path", container.BluetoothBusSocketPath,
		"--upstream-address", "unix:path=" + hostSystemBusPath(),
	}
	return startBusProxy(container.LogPath, container.BluetoothBusSocketPath, "Bluetooth bus", arguments)
}

func startBusProxy(logPath, socketPath, name string, arguments []string) (int, error) {
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return 0, err
	}
	command := exec.Command(cpakBinary, arguments...)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("open %s log: %w", name, err)
	}
	defer logFile.Close()
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return 0, fmt.Errorf("start %s proxy: %w", name, err)
	}
	pid := command.Process.Pid
	_ = command.Process.Release()
	if err = waitForSocket(socketPath, 3*time.Second); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return 0, fmt.Errorf("start %s proxy: %w", name, err)
	}
	return pid, nil
}

func cleanupDesktopBusProxy(container types.Container) {
	if sameRecordedProcess(container.DesktopBusProxyPid, container.DesktopBusProxyStartTime) {
		_ = syscall.Kill(container.DesktopBusProxyPid, syscall.SIGTERM)
	}
	if container.DesktopBusSocketPath != "" {
		_ = os.Remove(container.DesktopBusSocketPath)
	}
}

func cleanupBluetoothBusProxy(container types.Container) {
	if sameRecordedProcess(container.BluetoothBusProxyPid, container.BluetoothBusProxyStartTime) {
		_ = syscall.Kill(container.BluetoothBusProxyPid, syscall.SIGTERM)
	}
	if container.BluetoothBusSocketPath != "" {
		_ = os.Remove(container.BluetoothBusSocketPath)
	}
}

func hostSessionBusPath() string {
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "bus")
}

func hostSystemBusPath() string {
	return "/run/dbus/system_bus_socket"
}

func privateDesktopLinks(container types.Container) []string {
	links := []string{}
	if container.BluetoothBusSocketPath != "" {
		links = append(links, container.BluetoothBusSocketPath+":"+hostSystemBusPath())
	}
	if container.X11SocketPath != "" {
		links = append(links,
			container.X11SocketPath+":"+container.X11SocketTarget,
			container.X11AuthorityPath+":"+x11AuthorityTarget,
		)
	}
	return links
}

func withoutMount(mounts []string, excluded string) []string {
	result := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if filepath.Clean(mount) != filepath.Clean(excluded) {
			result = append(result, mount)
		}
	}
	return result
}

func (c *Cpak) privateApplicationHome(applicationID string) (string, error) {
	dataPath, err := c.applicationDataPath(applicationID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataPath, "home")
	if err := securePrivateDirectory(path); err != nil {
		return "", fmt.Errorf("prepare private application home: %w", err)
	}
	return path, nil
}

func (c *Cpak) applicationDataPath(applicationID string) (string, error) {
	if strings.TrimSpace(applicationID) == "" || applicationID == "." || applicationID == ".." || strings.ContainsAny(applicationID, "/\\\x00") {
		return "", errors.New("application ID is invalid")
	}
	return c.GetInStoreDir("application-data", applicationID), nil
}

// filesystemIncludesHostHome answers whether an application already holds the
// real home. The legacy fields count: an application installed before cpak
// migrated them still mounts the home as a plain mount, and a private home
// handed out beside it is mounted over rather than isolating anything.
func filesystemIncludesHostHome(override types.Override) bool {
	if override.FsHostHome {
		return true
	}
	for _, permission := range override.Filesystem {
		if permission.Path == "home" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	for _, path := range override.FsExtra {
		if filepath.Clean(path) == filepath.Clean(home) {
			return true
		}
	}
	return false
}

func (c *Cpak) cleanupNestedContainer(container types.Container) {
	terminateContainerProcess(container)
	if err := c.CleanupContainer(container); err != nil {
		logger.Printf("Cannot clean nested container %s: %v", container.CpakId, err)
	}
}

// getCpakBinary returns the canonical path to the running executable. The
// result is used to re-enter cpak outside the container root, so it must not be
// selected from argv, PATH or another location writable by a package.
func getCpakBinary() (cpakBinary string, err error) {
	cpakBinary, err = os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate cpak executable: %w", err)
	}
	cpakBinary, err = filepath.EvalSymlinks(cpakBinary)
	if err != nil {
		return "", fmt.Errorf("resolve cpak executable: %w", err)
	}
	return cpakBinary, nil
}

var nestedMarkerPath = "/tmp/.cpak"

// getNested answers the capability this container was given, and whether it is
// running inside one at all. It used to answer the parent's identifier, which
// the caller then sent as its own proof of identity.
func getNested() (token string, nested bool) {
	return readNestedToken()
}

const systemBrokerSocketTarget = "/run/cpak/system-broker.sock"
const systemBrokerTokenTarget = "/run/cpak/system-broker.token"

func createSystemBrokerRuntime(statePath string) (string, string, error) {
	socketPath, err := sharedSystemBrokerSocketPath()
	if err != nil {
		return "", "", err
	}
	if err = securePrivateDirectory(statePath); err != nil {
		return "", "", fmt.Errorf("prepare system broker state: %w", err)
	}
	return socketPath, filepath.Join(statePath, "system-broker.token"), nil
}

func sharedSystemBrokerSocketPath() (string, error) {
	directory, err := sharedSystemBrokerRuntimeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "system-broker.sock"), nil
}

func sharedSystemBrokerRuntimeDirectory() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	directory := ""
	if privateRuntimeDirectory(base) {
		directory = filepath.Join(base, "cpak")
	} else {
		directory = filepath.Join(os.TempDir(), fmt.Sprintf("cpak-%d", os.Getuid()))
	}
	if err := securePrivateDirectory(directory); err != nil {
		return "", fmt.Errorf("prepare system broker runtime: %w", err)
	}
	return directory, nil
}

func securePrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a directory this user can keep private", path)
	}
	if stat.Uid != uint32(os.Getuid()) {
		// This gate sits under the store paths, so a single sudo run leaves
		// every later cpak command failing here. It names the path, who owns
		// it and how to hand it back, because the user has nothing else to
		// go on.
		return fmt.Errorf("%s is owned by uid %d, not by uid %d: run chown -R %d %s to hand it back", path, stat.Uid, os.Getuid(), os.Getuid(), path)
	}
	if info.Mode().Perm() != 0700 {
		if err = os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	return nil
}

// provePrivateDirectory answers whether a directory cpak did not make is
// already private, and it changes nothing on its way to the answer.
//
// securePrivateDirectory is the wrong tool for a path somebody else named: it
// creates what is missing and tightens what it finds, which is right for the
// directories cpak owns and is a mutation of the caller's machine anywhere
// else. A CPAK_SERVICE_SOCKET pointing at $HOME/cpak.sock would have had the
// home directory chmodded to 0700 as a side effect of being checked, and a
// root caller, whose uid matches every root-owned directory, would have taken
// /tmp down to 0700 with every other account on it.
//
// So this proves and refuses. The path is looked at with Lstat, because a
// symlink to a private directory is not one, and the mode has to be exactly
// 0700 rather than merely closed to others: a directory this account may not
// write is not one this account can hold a socket in either.
// adoptPrivateDirectory makes a directory cpak was pointed at, without taking
// it over.
//
// cpak creating the directory is cpak choosing its mode, so a missing one is
// made private. One that is already there was made by somebody else, and its
// mode is theirs: it is proven and reported, never changed.
func adoptPrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	return provePrivateDirectory(path)
}

func provePrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("private directory is not owned by the current user")
	}
	if info.Mode().Perm() != 0700 {
		return fmt.Errorf("private directory %s is open to other accounts: %s", path, info.Mode().Perm())
	}
	return nil
}

// securePrivateDirectoryUnder secures every directory below root down to path.
//
// securePrivateDirectory answers only for the directory it is handed, and
// MkdirAll leaves a directory that already exists at whatever mode it has, so
// on an installation made by an older cpak the leaves become private while
// every directory above them stays world-readable. Walking the spine is what
// makes an upgraded tree converge.
//
// The root itself is not touched. Every root passed here is one of the paths
// cpak.json and the CPAK_ variables can move, and Options.keepPrivate has
// already answered for it under the rule that fits where it came from. Doing
// it again here would narrow a directory the operator named, whichever way
// that was decided.
func securePrivateDirectoryUnder(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := securePrivateDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func privateRuntimeDirectory(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func cleanupSystemBrokerRuntime(container types.Container) {
	if container.SystemBrokerPolicyPath != "" {
		_ = os.Remove(container.SystemBrokerPolicyPath)
	}
	if container.SystemBrokerTokenPath != "" {
		_ = os.Remove(container.SystemBrokerTokenPath)
	}
	if directory := filepath.Dir(container.SystemBrokerSocketPath); strings.HasPrefix(filepath.Base(directory), "cpak-broker-") {
		_ = os.RemoveAll(directory)
	}
}

func desktopRuntimePath(statePath string) string {
	return filepath.Join(statePath, "desktop-runtime")
}

func createDesktopRuntime(statePath string) (string, error) {
	path := desktopRuntimePath(statePath)
	if err := securePrivateDirectory(path); err != nil {
		return "", fmt.Errorf("create desktop runtime: %w", err)
	}
	return path, nil
}

func writeSystemBrokerToken(path string) error {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return fmt.Errorf("generate system broker token: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create system broker token: %w", err)
	}
	if _, err = file.WriteString(hex.EncodeToString(data)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write system broker token: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("write system broker token: %w", err)
	}
	return nil
}

func systemBrokerPolicyDirectory() (string, error) {
	runtimeDirectory, err := sharedSystemBrokerRuntimeDirectory()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(runtimeDirectory, "policies")
	if err = securePrivateDirectory(directory); err != nil {
		return "", fmt.Errorf("prepare system broker policy directory: %w", err)
	}
	return directory, nil
}

func (c *Cpak) registerSystemBrokerPolicy(tokenPath, desktopRuntime, owner, filePickerApplication, filePickerOrigin string, override types.Override, statePath, grantSocketPath string) (string, error) {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("read system broker token: %w", err)
	}
	applications := map[string]string(nil)
	if override.HostApplications {
		_, catalogPath := hostApplicationCatalogPaths(statePath)
		applications, err = systembroker.LoadApplicationCatalog(catalogPath)
		if err != nil {
			return "", err
		}
	}
	capabilities := types.HostActionCapabilities(override.HostActions, types.HostActionProviderContainers)
	paths := []systembroker.ContainerPathGrant(nil)
	if len(capabilities) > 0 {
		paths, err = systemBrokerContainerPaths(override.Filesystem)
		if err != nil {
			return "", err
		}
	}
	filePickerPaths := []systembroker.FilePickerPathGrant(nil)
	if override.FilePicker.Enabled() {
		filePickerPaths, err = systemBrokerFilePickerPaths(override.Filesystem)
		if err != nil {
			return "", err
		}
	}
	policy := systembroker.Policy{
		AllowNotify:           override.Notification,
		AllowOpenURI:          override.OpenURI,
		AllowHostApplications: override.HostApplications,
		Applications:          applications,
		RuntimeDirectory:      desktopRuntime,
		ContainerOwner:        owner,
		ContainerCapabilities: capabilities,
		ContainerPaths:        paths,
		FilePicker: systembroker.FilePickerPolicy{
			OpenFile:         override.FilePicker.OpenFile,
			OpenFolder:       override.FilePicker.OpenFolder,
			SaveFile:         override.FilePicker.SaveFile,
			Persistent:       override.FilePicker.Persistent,
			ContainingFolder: override.FilePicker.ContainingFolder,
		},
		FilePickerPaths:       filePickerPaths,
		FilePickerApplication: filePickerApplication,
		FilePickerOrigin:      filePickerOrigin,
		FileGrantSocketPath:   grantSocketPath,
		FileGrantStorePath:    filepath.Join(c.Options.StorePath, "grants"),
	}
	directory, err := systemBrokerPolicyDirectory()
	if err != nil {
		return "", err
	}
	if err = systembroker.WritePolicy(directory, string(token), policy); err != nil {
		return "", err
	}
	return systembroker.PolicyPath(directory, string(token))
}

func systemBrokerFilePickerPaths(permissions []types.FilesystemPermission) ([]systembroker.FilePickerPathGrant, error) {
	paths := make([]systembroker.FilePickerPathGrant, 0, len(permissions))
	for _, permission := range permissions {
		source, target, err := types.ResolveFilesystemPermission(permission)
		if errors.Is(err, types.ErrXDGUserDirectoryUnavailable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(source)
		if os.IsNotExist(err) && strings.HasPrefix(permission.Path, "xdg-") {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve file picker path: %w", err)
		}
		paths = append(paths, systembroker.FilePickerPathGrant{
			Source:   resolved,
			Target:   target,
			ReadOnly: permission.Access == "read-only",
		})
	}
	return paths, nil
}

func (c *Cpak) mountPersistentFileGrants(origin string, container types.Container) error {
	store := filegrant.Store{Directory: filepath.Join(c.Options.StorePath, "grants")}
	grants, err := store.Load(origin)
	if err != nil {
		return err
	}
	grantSocketPath := container.GrantSocketPath
	if grantSocketPath == "" {
		grantSocketPath = filepath.Join(container.StatePath, "grant.sock")
	}
	for _, grant := range grants {
		source, openErr := filegrant.OpenSource(grant)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		mountSource, mountOpenErr := filegrant.OpenMountSource(grant)
		if mountOpenErr != nil {
			_ = source.Close()
			return mountOpenErr
		}
		_, sendErr := grantproto.Send(grantSocketPath, grant, source, mountSource)
		closeErr := source.Close()
		if mountSource != nil {
			if err = mountSource.Close(); closeErr == nil {
				closeErr = err
			}
		}
		if sendErr != nil {
			return sendErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func systemBrokerContainerPaths(permissions []types.FilesystemPermission) ([]systembroker.ContainerPathGrant, error) {
	paths := make([]systembroker.ContainerPathGrant, 0, len(permissions))
	for _, permission := range permissions {
		source, _, err := types.ResolveFilesystemPermission(permission)
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return nil, fmt.Errorf("resolve container provider path: %w", err)
		}
		paths = append(paths, systembroker.ContainerPathGrant{Path: resolved, ReadOnly: permission.Access == "read-only"})
	}
	return paths, nil
}
