/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cpak

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/uuid"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
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
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

	// Check if a container already exists for the given application
	containers, err := store.GetApplicationContainers(app)
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
		fmt.Println("Container found:", container.CpakId)

		// If the container is not running, we clean it up and create a new one
		// by escaping the if statement
		container.Pid, err = getPidFromEnvContainerId(container.CpakId)
		if err != nil || container.Pid == 0 {
			fmt.Println("Container not running, cleaning it up:", container.CpakId)
			err = c.CleanupContainer(container)
			if err != nil {
				return
			}
		} else {
			fmt.Println("Container already running, attaching to it:", container.CpakId)
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
		ApplicationCpakId: app.CpakId,
		StatePath:         statePath,
		CreateTimestamp:   time.Now(),
	}

	container.HostExecSocketPath = filepath.Join(container.StatePath, "hostexec.sock")

	// Start the hostexec server process
	container.HostExecPid, err = c.startHostExecServerProcess(container.HostExecSocketPath, override.AllowedHostCommands)
	if err != nil {
		fmt.Println("Error starting hostexec server, cleaning up partially created container...")
		os.Remove(container.HostExecSocketPath)
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, fmt.Errorf("failed to start hostexec server: %w", err)
	}
	fmt.Println("HostExec server started (PID:", container.HostExecPid, "Socket:", container.HostExecSocketPath, ")")

	err = store.NewContainer(container)
	if err != nil {
		stopHostExecServer(container.HostExecPid)
		os.Remove(container.HostExecSocketPath)
		os.RemoveAll(c.GetInStoreDir("containers", container.CpakId))
		os.RemoveAll(container.StatePath)
		return types.Container{}, err
	}
	fmt.Println("Container created:", container.CpakId)

	_, container.Pid, err = c.StartContainer(container, app, config, override)
	if err != nil {
		c.CleanupContainer(container)
		return types.Container{}, err
	}

	fmt.Println("Container prepared:", container.CpakId)
	return
}

// StartContainer starts the container with the given config and image.
// The config is used to set the environment the way the developer wants.
// The container is started by calling our spawn function, which is the
// responsible for setting up the pivot root, mounting the layers and
// starting the init process, this via the rootlesskit binary which creates
// a new namespace for the container.
func (c *Cpak) StartContainer(container types.Container, app types.Application, config *v1.ConfigFile, override types.Override) (rootfs string, pid int, err error) {
	layers := ""
	for _, layer := range app.ParsedLayers {
		layers += layer + "|"
	}

	// the cpakBinary is the path to the cpak binary, it is used to re-execute
	// the cpak with the spawn command to start the container
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	layersPath := c.GetInStoreDir("layers")
	rootfs = c.GetInStoreDir("containers", container.CpakId, "rootfs")
	overrideMounts, overrideShims := GetOverrideMounts(override)
	cmds := []string{}
	if isVerbose {
		cmds = append(cmds, "--debug")
	}
	//"--net=slirp4netns",
	cmds = append(cmds, []string{
		"--cgroupns=true",
		"--utsns=true",
		"--ipcns=true",
		"--copy-up=/etc",
		"--propagation=rslave",
		cpakBinary,
		"spawn",
	}...)
	if isVerbose {
		cmds = append(cmds, "--verbose")
	}
	cmds = append(cmds, "--user-uid", uid)
	cmds = append(cmds, "--app-id", app.CpakId)
	cmds = append(cmds, "--container-id", container.CpakId)
	cmds = append(cmds, "--rootfs", rootfs)
	cmds = append(cmds, "--state-dir", container.StatePath)
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--layers-dir", layersPath)

	// Mount the main cpak binary into a known location inside the container
	cpakInContainerPath := "/usr/local/bin/cpak"
	cmds = append(cmds, "--extra-links", cpakBinary+":"+cpakInContainerPath)

	// Pass AllowedHostCommands and SocketPath via environment variables to spawn
	if container.HostExecSocketPath != "" {
		cmds = append(cmds, "--env", "CPAK_HOSTEXEC_SOCKET="+container.HostExecSocketPath)
	} else {
		log.Printf("Warning: HostExec socket path is empty for container %s during start.", container.CpakId)
	}
	// Join allowed commands into a single string (e.g., colon-separated) for the env var
	allowedCmdsStr := strings.Join(override.AllowedHostCommands, ":")
	cmds = append(cmds, "--env", "CPAK_ALLOWED_HOST_CMDS=xdg-open:"+allowedCmdsStr)

	for _, envVar := range config.Config.Env {
		cmds = append(cmds, "--env", envVar)
	}

	for _, ovr := range overrideMounts {
		cmds = append(cmds, "--mount-overrides", ovr)
	}

	for _, shim := range overrideShims {
		cmds = append(cmds, "--mount-shims", shim)
	}

	// following is where dependencies and addons are exported
	cmds = append(cmds, "--env", "PATH="+fmt.Sprintf("%s/%s", c.Options.ExportsPath, app.CpakId)+":$PATH")

	cmd := exec.Command(c.Options.RotlesskitBinPath, cmds...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), config.Config.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Foreground: false,
		Setsid:     true,
	}

	err = cmd.Run()
	if err != nil {
		return
	}

	// The pid of the container is the pid of the init process
	// and it is stored so that we can attach to it later
	pid, err = getPidFromEnvContainerId(container.CpakId)
	if err != nil {
		return
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

	err = store.SetContainerPid(container.CpakId, pid)
	if err != nil {
		return
	}
	return
}

// StopContainer stops the containers related to the given application.
func (c *Cpak) StopContainer(app types.Application) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		return
	}

	for _, container := range containers {
		currentPid := container.Pid
		if currentPid == 0 {
			currentPid, _ = getPidFromEnvContainerId(container.CpakId)
		}
		if currentPid != 0 {
			fmt.Println("Stopping container process:", currentPid)
			syscall.Kill(currentPid, syscall.SIGTERM)
		}
		cleanupErr := c.CleanupContainer(container)
		if cleanupErr != nil {
			fmt.Printf("Warning: error during container cleanup %s: %v\n", container.CpakId, cleanupErr)
		}
	}
	return
}

// Stop is a convenient wrapper around the StopContainer function that
// takes the origin and version of the application to stop.
func (c *Cpak) Stop(origin, version, branch, commit, release string) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}
	defer store.Close()

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil {
		return
	}
	if app.CpakId == "" {
		return fmt.Errorf("application not found for stopping: %s", origin)
	}

	err = c.StopContainer(app)
	if err != nil {
		return
	}
	return
}

// ExecInContainer uses nsenter to enter the pid namespace of the given
// container and execute the given command.
func (c *Cpak) ExecInContainer(app types.Application, container types.Container, command []string) (err error) {
	pidToEnter := container.Pid
	if pidToEnter == 0 {
		pidToEnter, err = getPidFromEnvContainerId(container.CpakId)
		if err != nil {
			return fmt.Errorf("container process %s not found: %w", container.CpakId, err)
		}
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())

	cmds := []string{
		"-m",
		"-u",
		"-U",
		"--preserve-credentials",
		"-i",
		//"-n",
		// "-p",
		// "-S", strconv.FormatInt(int64(os.Getuid()), 10),
		// "-G", strconv.FormatInt(int64(os.Getgid()), 10),
		"-t",
		fmt.Sprintf("%d", pidToEnter),
		"--",
	}

	if !app.ParsedOverride.AsRoot {
		cmds = append(
			cmds,
			"unshare",
			"-U",
			"--map-user="+uid,
			"--map-group="+gid,
			"--",
		)
	}
	cmds = append(cmds, command...)

	envVars := os.Environ()
	envVars = append(envVars, "CPAK_CONTAINER_ID="+container.CpakId)
	envVars = append(envVars, "CPAK_HOSTEXEC_SOCKET="+container.HostExecSocketPath)

	cmd := exec.Command(c.Options.NsenterBinPath, cmds...)
	fmt.Println("Executing command:", cmd.String())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = envVars

	err = cmd.Run()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			if exitErr.ExitCode() == 2 {
				err = nil
			}
		}
		return
	}
	return
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
		fmt.Println("PID found:", pid)
	}
	return
}

// CleanupContainer removes the container with the given id.
func (c *Cpak) CleanupContainer(container types.Container) (err error) {
	// Stop hostexec server first
	stopHostExecServer(container.HostExecPid)

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

// getCpakBinary returns the path to the cpak binary.
func getCpakBinary() (cpakBinary string, err error) {
	cpakBinary = os.Args[0]
	// if the cpak binary is not a full path, we need to find it
	if !filepath.IsAbs(cpakBinary) {
		cpakBinaryExe, findErr := exec.LookPath("cpak")
		if findErr == nil {
			cpakBinary = cpakBinaryExe
		} else {
			homeDir, homeErr := os.UserHomeDir()
			if homeErr == nil {
				userPath := filepath.Join(homeDir, ".local", "bin", "cpak")
				if _, statErr := os.Stat(userPath); statErr == nil {
					cpakBinary = userPath
				} else {
					return "", fmt.Errorf("cpak binary not found in PATH or ~/.local/bin: %v, %v", findErr, statErr)
				}
			} else {
				return "", fmt.Errorf("cpak binary not found in PATH and UserHomeDir failed: %v, %v", findErr, homeErr)
			}
		}
	}
	return
}

// getNested checks if the /tmp/.cpak file exists and returns the parent
// application id from it.
func getNested() (parentAppCpakId string, nested bool) {
	nested = false
	parentAppCpakId = ""
	if _, err := os.Stat("/tmp/.cpak"); err == nil {
		nested = true
		file, errOpen := os.Open("/tmp/.cpak")
		if errOpen != nil {
			return parentAppCpakId, true
		}
		defer file.Close()

		_, errScan := fmt.Fscanln(file, &parentAppCpakId)
		if errScan != nil {
			return parentAppCpakId, true
		}
	}
	return
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

	cmd := exec.Command(cpakBinary, args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	// Detach the process completely
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	fmt.Printf("Starting hostexec server: %s %v\n", cpakBinary, args)
	err = cmd.Start()
	if err != nil {
		logF.Close()
		return 0, fmt.Errorf("failed to start hostexec server process: %w", err)
	}

	pid = cmd.Process.Pid
	fmt.Printf("Hostexec server process started with PID: %d, logging to %s\n", pid, logFile)

	// Release the process handle so the parent (this function) can return.
	err = cmd.Process.Release()
	if err != nil {
		log.Printf("Warning: Failed to release hostexec server process %d: %v. Attempting to kill.", pid, err)
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
		logF.Close()
		return 0, fmt.Errorf("failed to release hostexec server process %d: %w", pid, err)
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
		log.Printf("Stopping hostexec server (PID: %d)...", pid)
		err = process.Signal(syscall.SIGTERM)
		if err != nil {
			if !strings.Contains(err.Error(), "process already finished") && !strings.Contains(err.Error(), "no such process") {
				log.Printf("Failed to send SIGTERM to hostexec server %d: %v.", pid, err)
			}
		} else {
			log.Printf("Sent SIGTERM to hostexec server %d.", pid)
		}
	} else {
		log.Printf("Hostexec server process %d not found (already stopped?).", pid)
	}
}
