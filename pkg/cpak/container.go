/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/uuid"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
	"github.com/mirkobrombin/cpak/pkg/tools"
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
	return c.prepareContainer(app, override, app.CpakId, "")
}

func (c *Cpak) PrepareContainerInstance(app types.Application, override types.Override, instance string) (types.Container, error) {
	scope := ApplicationScope(app.CpakId, instance)
	return c.prepareContainer(app, override, scope, instance)
}

func ApplicationScope(applicationCpakId, instance string) string {
	if instance == "" {
		return applicationCpakId
	}
	return applicationCpakId + ":instance:" + instance
}

func (c *Cpak) PrepareNestedContainer(app types.Application, override types.Override) (types.Container, error) {
	scope := app.CpakId + ":nested:" + uuid.NewString()
	return c.prepareContainer(app, override, scope, "")
}

func (c *Cpak) prepareContainer(app types.Application, override types.Override, scope, instance string) (container types.Container, err error) {
	unlock, err := c.lockContainerScope(scope)
	if err != nil {
		return types.Container{}, err
	}
	defer unlock()

	addons, err := c.resolveEnabledAddons(app)
	if err != nil {
		return types.Container{}, err
	}
	policyHash, err := containerPolicyHash(override, addons...)
	if err != nil {
		return types.Container{}, err
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()

	// Check if a container already exists for the given application
	scopedApp := app
	scopedApp.CpakId = scope
	containers, err := store.GetApplicationContainers(scopedApp)
	if err != nil {
		return
	}

	config := &v1.ConfigFile{}
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
		if container.PolicyHash != policyHash || !containerProcessRunning(container) {
			container.Pid, err = getPidFromEnvContainerId(container.CpakId)
		}
		if container.PolicyHash != policyHash || !containerProcessRunning(container) {
			logger.Println("Container cannot be reused, cleaning it up:", container.CpakId)
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

	container.HostExecSocketPath = filepath.Join(container.StatePath, "hostexec.sock")
	container.ExecSocketPath = filepath.Join(container.StatePath, "exec.sock")

	hostCommands := effectiveHostCommands(override)
	if len(hostCommands) > 0 {
		container.HostExecPid, err = c.startHostExecServerProcess(container.HostExecSocketPath, hostCommands)
		if err != nil {
			logger.Println("Error starting hostexec server, cleaning up partially created container...")
			os.Remove(container.HostExecSocketPath)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, fmt.Errorf("failed to start hostexec server: %w", err)
		}
		logger.Println("HostExec server started (PID:", container.HostExecPid, "Socket:", container.HostExecSocketPath, ")")
	} else {
		container.HostExecSocketPath = ""
	}
	if len(systemBrokerOperations(override)) > 0 {
		container.SystemBrokerSocketPath, container.SystemBrokerTokenPath, err = createSystemBrokerRuntime()
		if err != nil {
			stopHostExecServer(container.HostExecPid)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		if err = writeSystemBrokerToken(container.SystemBrokerTokenPath); err != nil {
			stopHostExecServer(container.HostExecPid)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, err
		}
		container.SystemBrokerPid, err = c.startSystemBrokerProcess(container.SystemBrokerSocketPath, container.SystemBrokerTokenPath, filepath.Join(container.StatePath, "system-broker.log"), override)
		if err != nil {
			stopHostExecServer(container.HostExecPid)
			cleanupSystemBrokerRuntime(container)
			os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
			os.RemoveAll(container.StatePath)
			return types.Container{}, fmt.Errorf("failed to start system broker: %w", err)
		}
	} else {
		container.SystemBrokerSocketPath = ""
		container.SystemBrokerTokenPath = ""
	}

	err = store.NewContainer(container)
	if err != nil {
		stopHostExecServer(container.HostExecPid)
		stopSystemBroker(container.SystemBrokerPid)
		os.Remove(container.HostExecSocketPath)
		cleanupSystemBrokerRuntime(container)
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, err
	}
	logger.Println("Container created:", container.CpakId)
	if err = store.Close(); err != nil {
		return types.Container{}, err
	}
	store = nil

	_, container.Pid, container.CgroupPath, err = c.StartContainer(container, app, addons, config, override)
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}
	store, err = NewStore(c.Options.StorePath)
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}
	if err = store.SetContainerRuntime(container.CpakId, container.Pid, container.CgroupPath); err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}

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

func containerPolicyHash(override types.Override, addons ...types.Application) (string, error) {
	policy := struct {
		Override types.Override        `json:"override"`
		Addons   []addonPolicyIdentity `json:"addons,omitempty"`
	}{
		Override: override,
		Addons:   addonPolicyIdentities(addons),
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode container policy: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", hash[:]), nil
}

func effectiveHostCommands(override types.Override) []string {
	_, shims := GetOverrideMounts(override)
	commands := append([]string{}, override.AllowedHostCommands...)
	commands = append(commands, shims...)
	seen := make(map[string]bool, len(commands))
	result := make([]string, 0, len(commands))
	for _, command := range commands {
		if command == "" || seen[command] {
			continue
		}
		seen[command] = true
		result = append(result, command)
	}
	return result
}

// StartContainer starts the container with the given config and image.
// The config is used to set the environment the way the developer wants.
// The container is started by calling our spawn function, which is the
// responsible for setting up the pivot root, mounting the layers and
// replacing itself with the init process inside native Linux namespaces.
func (c *Cpak) StartContainer(container types.Container, app types.Application, addons []types.Application, config *v1.ConfigFile, override types.Override) (rootfs string, pid int, cgroupPath string, err error) {
	layers := ""
	for _, layer := range combinedLayers(app, addons) {
		layers += layer + "|"
	}

	// the cpakBinary is the path to the cpak binary, it is used to re-execute
	// the cpak with the spawn command to start the container
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	layersPath := c.GetInStoreDir("layers")
	rootfs = c.GetInStoreDir("containers", container.CpakId, "rootfs")
	overrideMounts, overrideShims := GetOverrideMounts(override)
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
	cmds = append(cmds, "--container-id", container.CpakId)
	cmds = append(cmds, "--rootfs", rootfs)
	cmds = append(cmds, "--state-dir", container.StatePath)
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--layers-dir", layersPath)
	cmds = append(cmds, "--ready-fd", "3")
	cmds = append(cmds, "--exec-socket", container.ExecSocketPath)
	cmds = append(cmds, "--idle-time", strconv.Itoa(app.IdleTime))
	if override.FsHost {
		cmds = append(cmds, "--mount-host-root")
	}
	if override.DeviceDri || override.DeviceAll {
		cmds = append(cmds, "--nvidia")
	}
	if override.UserNamespaces {
		cmds = append(cmds, "--user-namespaces")
	}

	// Mount the main cpak binary into a known location inside the container
	cpakInContainerPath := "/usr/local/bin/cpak"
	cmds = append(cmds, "--extra-links", cpakBinary+":"+cpakInContainerPath)
	dependencyLinks, err := c.dependencyLinks(app)
	if err != nil {
		return "", 0, "", err
	}
	for _, link := range dependencyLinks {
		cmds = append(cmds, "--extra-links", link)
	}

	// Pass AllowedHostCommands and SocketPath via environment variables to spawn
	if container.HostExecSocketPath != "" {
		cmds = append(cmds, "--env", "CPAK_HOSTEXEC_SOCKET="+container.HostExecSocketPath)
	}
	if container.SystemBrokerSocketPath != "" {
		cmds = append(cmds, "--env", "CPAK_SYSTEM_BROKER_SOCKET="+systemBrokerSocketTarget)
		cmds = append(cmds, "--env", "CPAK_SYSTEM_BROKER_TOKEN_FILE="+systemBrokerTokenTarget)
		cmds = append(cmds, "--extra-links", container.SystemBrokerSocketPath+":"+systemBrokerSocketTarget)
		cmds = append(cmds, "--extra-links", container.SystemBrokerTokenPath+":"+systemBrokerTokenTarget)
	}
	// Join allowed commands into a single string (e.g., colon-separated) for the env var
	allowedCmdsStr := strings.Join(effectiveHostCommands(override), ":")
	if allowedCmdsStr != "" {
		cmds = append(cmds, "--env", "CPAK_ALLOWED_HOST_CMDS="+allowedCmdsStr)
	}

	containerEnv := append([]string{}, config.Config.Env...)
	containerEnv = append(containerEnv, override.Env...)
	containerEnv = append(containerEnv, "CPAK_SERVICE_SOCKET="+defaultCpakSocketPath)
	for _, envVar := range containerEnv {
		cmds = append(cmds, "--env", envVar)
	}

	for _, ovr := range overrideMounts {
		cmds = append(cmds, "--mount-overrides", ovr)
	}

	for _, shim := range overrideShims {
		cmds = append(cmds, "--mount-shims", shim)
	}
	for _, shim := range systemBrokerOperations(override) {
		cmds = append(cmds, "--system-shims", shim)
	}

	cmds = append(cmds, "--env", "PATH="+buildContainerPath(config.Config.Env))

	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return "", 0, "", fmt.Errorf("create readiness pipe: %w", err)
	}
	defer readyReader.Close()
	defer readyWriter.Close()

	cmd := nativeNamespaceCommand(cpakBinary, cmds, namespaceOptions{
		IsolateNetwork: !override.Network,
		ShareProcesses: override.Process,
		IsolateCgroup:  true,
	})
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), containerEnv...)
	cmd.Env = append(cmd.Env, "CPAK_CONTAINER_ID="+container.CpakId)
	cmd.ExtraFiles = []*os.File{readyWriter}

	if err = cmd.Start(); err != nil {
		return "", 0, "", fmt.Errorf("start container namespace: %w", err)
	}
	_ = readyWriter.Close()

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

	select {
	case err = <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			return "", 0, "", fmt.Errorf("container failed before readiness: %w", err)
		}
	case err = <-exited:
		if err == nil {
			err = fmt.Errorf("container init exited before readiness")
		}
		return "", 0, "", err
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		return "", 0, "", fmt.Errorf("container readiness timed out")
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
		currentPid := container.Pid
		if currentPid == 0 {
			currentPid, _ = getPidFromEnvContainerId(container.CpakId)
		}
		if currentPid != 0 {
			logger.Println("Stopping container process:", currentPid)
			syscall.Kill(currentPid, syscall.SIGTERM)
		}
		cleanupErr := c.CleanupContainer(container)
		if cleanupErr != nil {
			logger.Printf("Warning: error during container cleanup %s: %v", container.CpakId, cleanupErr)
		}
	}
	return
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
func (c *Cpak) ExecInContainer(app types.Application, container types.Container, command []string) (err error) {
	pidToEnter := container.Pid
	if pidToEnter == 0 {
		pidToEnter, err = getPidFromEnvContainerId(container.CpakId)
		if err != nil {
			return fmt.Errorf("container process %s not found: %w", container.CpakId, err)
		}
	}

	override := resolvedOverride(app)

	envVars, err := containerEnvironment(app, container)
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

func containerEnvironment(app types.Application, container types.Container) ([]string, error) {
	config := &v1.ConfigFile{}
	if err := json.Unmarshal([]byte(app.Config), config); err != nil {
		return nil, fmt.Errorf("decode application config: %w", err)
	}

	envVars := append([]string{}, os.Environ()...)
	envVars = append(envVars, config.Config.Env...)
	envVars = append(envVars, resolvedOverride(app).Env...)
	envVars = append(envVars, "CPAK_CONTAINER_ID="+container.CpakId)
	envVars = append(envVars, "CPAK_HOSTEXEC_SOCKET="+container.HostExecSocketPath)
	if container.SystemBrokerSocketPath != "" {
		envVars = append(envVars, "CPAK_SYSTEM_BROKER_SOCKET="+systemBrokerSocketTarget)
		envVars = append(envVars, "CPAK_SYSTEM_BROKER_TOKEN_FILE="+systemBrokerTokenTarget)
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

// getPidFromEnvContainerId returns the pid of the process with the given containerId
// by looking at the environment variables of all the processes.
func getPidFromEnvContainerId(containerCpakId string) (pid int, err error) {
	env := "CPAK_CONTAINER_ID=" + containerCpakId
	pid, err = tools.GetPidFromEnv(env)
	if err != nil {
		err = fmt.Errorf("no process with containerId %s found", containerCpakId)
		return
	}
	if isVerbose {
		logger.Println("PID found:", pid)
	}
	return
}

func containerProcessRunning(container types.Container) bool {
	if container.Pid <= 0 || syscall.Kill(container.Pid, 0) != nil {
		return false
	}
	execSocketPath := container.ExecSocketPath
	if execSocketPath == "" {
		execSocketPath = filepath.Join(container.StatePath, "exec.sock")
	}
	return socketIsLive(execSocketPath)
}

// CleanupContainer removes the container with the given id.
func (c *Cpak) CleanupContainer(container types.Container) (err error) {
	// Stop hostexec server first
	stopHostExecServer(container.HostExecPid)
	stopSystemBroker(container.SystemBrokerPid)
	cleanupSystemBrokerRuntime(container)
	cleanupCgroup(container.CpakId, container.CgroupPath)

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

func (c *Cpak) cleanupNestedContainer(container types.Container) {
	if container.Pid != 0 {
		_ = syscall.Kill(container.Pid, syscall.SIGTERM)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := getPidFromEnvContainerId(container.CpakId); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := c.CleanupContainer(container); err != nil {
		logger.Printf("Cannot clean nested container %s: %v", container.CpakId, err)
	}
}

// getCpakBinary returns the path to the cpak binary.
func getCpakBinary() (cpakBinary string, err error) {
	if strings.ContainsRune(os.Args[0], os.PathSeparator) {
		cpakBinary, err = filepath.Abs(os.Args[0])
		if err != nil {
			return "", fmt.Errorf("resolve cpak executable path: %w", err)
		}
		if _, statErr := os.Stat(cpakBinary); statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(cpakBinary)
			if resolveErr == nil {
				cpakBinary = resolved
			}
			return cpakBinary, nil
		}
	}
	cpakBinary, err = os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate cpak executable: %w", err)
	}
	resolved, resolveErr := filepath.EvalSymlinks(cpakBinary)
	if resolveErr == nil {
		cpakBinary = resolved
	}
	return cpakBinary, nil
}

var nestedMarkerPath = "/tmp/.cpak"

func getNested() (parentAppCpakId string, nested bool) {
	content, err := os.ReadFile(nestedMarkerPath)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(content)), true
}

// startHostExecServerProcess starts the 'cpak hostexec-server' in the background.
// It redirects server logs to a file within the container's state directory.
func (c *Cpak) startHostExecServerProcess(socketPath string, allowedCmds []string) (pid int, err error) {
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return 0, fmt.Errorf("cannot find cpak binary for hostexec server: %w", err)
	}

	args := []string{
		"hostexec-server",
		"--socket-path", socketPath,
	}
	for _, cmdName := range allowedCmds {
		if cmdName != "" {
			args = append(args, "--allowed-cmd", cmdName)
		}
	}

	// Log file setup (use container state dir for logs)
	logDir := filepath.Dir(socketPath)
	logFile := filepath.Join(logDir, "hostexec-server.log")

	// Ensure log directory exists (it should, as statePath is created earlier)
	if err := os.MkdirAll(logDir, 0700); err != nil && !os.IsExist(err) {
		return 0, fmt.Errorf("failed to ensure log directory %s for hostexec server: %w", logDir, err)
	}

	logF, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("failed to open log file %s for hostexec server: %w", logFile, err)
	}
	defer logF.Close()

	cmd := exec.Command(cpakBinary, args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	// Detach the process completely
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	logger.Printf("Starting hostexec server: %s %v", cpakBinary, args)
	err = cmd.Start()
	if err != nil {
		return 0, fmt.Errorf("failed to start hostexec server process: %w", err)
	}

	pid = cmd.Process.Pid
	logger.Printf("Hostexec server process started with PID: %d, logging to %s", pid, logFile)

	// Release the process handle so the parent (this function) can return.
	err = cmd.Process.Release()
	if err != nil {
		logger.Printf("Warning: Failed to release hostexec server process %d: %v. Attempting to kill.", pid, err)
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
		return 0, fmt.Errorf("failed to release hostexec server process %d: %w", pid, err)
	}
	if err = waitForSocket(socketPath, socketWaitTimeout); err != nil {
		stopHostExecServer(pid)
		detail := ""
		if content, readErr := os.ReadFile(logFile); readErr == nil {
			detail = strings.TrimSpace(string(content))
		}
		if detail != "" {
			return 0, fmt.Errorf("hostexec server did not become ready: %s: %w", detail, err)
		}
		return 0, fmt.Errorf("hostexec server did not become ready: %w", err)
	}
	return pid, nil
}

// stopHostExecServer sends SIGTERM to the hostexec server process.
func stopHostExecServer(pid int) {
	if pid == 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		logger.Printf("Stopping hostexec server (PID: %d)...", pid)
		err = process.Signal(syscall.SIGTERM)
		if err != nil {
			if !strings.Contains(err.Error(), "process already finished") && !strings.Contains(err.Error(), "no such process") {
				logger.Printf("Failed to send SIGTERM to hostexec server %d: %v.", pid, err)
			}
		} else {
			logger.Printf("Sent SIGTERM to hostexec server %d.", pid)
		}
	} else {
		logger.Printf("Hostexec server process %d not found (already stopped?).", pid)
	}
}

const systemBrokerSocketTarget = "/run/cpak/system-broker.sock"
const systemBrokerTokenTarget = "/run/cpak/system-broker.token"

func createSystemBrokerRuntime() (string, string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if !privateRuntimeDirectory(base) {
		base = os.TempDir()
	}
	directory, err := os.MkdirTemp(base, "cpak-broker-")
	if err != nil {
		return "", "", fmt.Errorf("create system broker runtime: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		_ = os.Remove(directory)
		return "", "", fmt.Errorf("restrict system broker runtime: %w", err)
	}
	return filepath.Join(directory, "broker.sock"), filepath.Join(directory, "token"), nil
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
	if container.SystemBrokerSocketPath != "" {
		_ = os.Remove(container.SystemBrokerSocketPath)
	}
	if container.SystemBrokerTokenPath != "" {
		_ = os.Remove(container.SystemBrokerTokenPath)
	}
	if container.SystemBrokerSocketPath != "" {
		_ = os.Remove(filepath.Dir(container.SystemBrokerSocketPath))
	}
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

func (c *Cpak) startSystemBrokerProcess(socketPath, tokenPath, logPath string, override types.Override) (int, error) {
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return 0, fmt.Errorf("cannot find cpak binary for system broker: %w", err)
	}
	args := []string{"system-broker-server", "--socket-path", socketPath, "--token-file", tokenPath}
	if override.Notification {
		args = append(args, "--notify")
	}
	if override.OpenURI {
		args = append(args, "--open-uri")
	}
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 0, fmt.Errorf("open system broker log: %w", err)
	}
	defer log.Close()
	command := exec.Command(cpakBinary, args...)
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, fmt.Errorf("start system broker: %w", err)
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		stopSystemBroker(pid)
		return 0, fmt.Errorf("release system broker process: %w", err)
	}
	if err := waitForSocket(socketPath, socketWaitTimeout); err != nil {
		stopSystemBroker(pid)
		return 0, fmt.Errorf("system broker did not become ready: %w", err)
	}
	return pid, nil
}

func stopSystemBroker(pid int) {
	if pid == 0 {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}
}
