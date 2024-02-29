package cpak

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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

	// Check if a container already exists for the given application
	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		return
	}

	container.Application = app
	config := &v1.ConfigFile{}
	err = json.Unmarshal([]byte(app.Config), config)
	if err != nil {
		return
	}

	// If a container already exists, check if it is running
	if len(containers) > 0 {
		container = containers[0]
		fmt.Println("Container found:", container.Id)

		container.StatePath, err = c.GetInStoreDirMkdir("states", container.Id)
		if err != nil {
			fmt.Println("Error getting state path:", err)
			return
		}
		// If the container is not running, we clean it up and create a new one
		// by escaping the if statement
		container.Pid, err = getPidFromEnvContainerId(container.Id)
		if err != nil || container.Pid == 0 {
			fmt.Println("Container not running, cleaning it up:", container.Id)
			err = c.CleanupContainer(container)
			if err != nil {
				return
			}
		} else {
			fmt.Println("Container already running, attaching to it:", container.Id)
			return
		}
	}

	// If no container exists, create a new one and store it
	// Note: the container's pid is not set here, it will be set when the
	// container is started by the StartContainer function
	container.Id, container.StatePath, err = c.CreateContainer()
	if err != nil {
		return
	}

	err = store.NewContainer(container)
	if err != nil {
		return
	}

	fmt.Println("Container created:", container.Id)

	// Start the container and return the pid
	_, container.Pid, err = c.StartContainer(container, config, override)
	if err != nil {
		return
	}

	fmt.Println("Container prepared:", container.Id)
	return
}

// StartContainer starts the container with the given config and image.
// The config is used to set the environment the way the developer wants.
// The container is started by calling our spawn function, which is the
// responsible for setting up the pivot root, mounting the layers and
// starting the init process, this via the rootlesskit binary which creates
// a new namespace for the container.
func (c *Cpak) StartContainer(container types.Container, config *v1.ConfigFile, override types.Override) (rootfs string, pid int, err error) {
	layers := ""
	for _, layer := range container.Application.Layers {
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
	rootfs = c.GetInStoreDir("containers", container.Id, "rootfs")
	overrideMounts := GetOverrideMounts(override)
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
	cmds = append(cmds, "--app-id", container.Application.Id)
	cmds = append(cmds, "--container-id", container.Id)
	cmds = append(cmds, "--rootfs", rootfs)
	cmds = append(cmds, "--state-dir", container.StatePath)
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--layers-dir", layersPath)
	// FIXME: exposing the host-spawn in xdg-open is needed for the browser to
	// be able to open the host's default browser, this is absolutely not
	// secure and should be changed in the future
	cmds = append(cmds, "--extra-links", c.Options.HostSpawnBinPath+":/usr/bin/xdg-open")
	if override.FsHost {
		cmds = append(cmds, "--extra-links", c.Options.HostSpawnBinPath+":/usr/bin/host-spawn")
	}

	for _, env := range config.Config.Env {
		cmds = append(cmds, "--env", env)
	}

	for _, override := range overrideMounts {
		cmds = append(cmds, "--mount-overrides", override)
	}

	// following is where dependencies and addons are exported
	cmds = append(cmds, "--env", "PATH="+fmt.Sprintf("%s/%s", c.Options.ExportsPath, container.Application.Id)+":$PATH")

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
	pid, err = getPidFromEnvContainerId(container.Id)
	if err != nil {
		return
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	err = store.SetContainerPid(container.Id, pid)
	if err != nil {
		return
	}

	return
}

// StopContainer stops the containers related to the given application.
func (c *Cpak) StopContainer(app types.Application, override types.Override) (err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		return
	}

	for _, container := range containers {
		fmt.Println("Stopping container:", container.Pid)
		syscall.Kill(container.Pid, syscall.SIGTERM)
		err = c.CleanupContainer(container)
		if err != nil {
			return
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

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil {
		return
	}

	override, err := LoadOverride(app.Origin, app.Version)
	if err != nil {
		override = app.Override
	}

	err = c.StopContainer(app, override)
	if err != nil {
		return
	}

	return
}

type ContainerCreateOptions struct {
	Entrypoint string
	Env        []string
	Volume     []string
}

func (c *Cpak) CreateContainer() (containerId string, statePath string, err error) {
	containerId = uuid.New().String()

	_, err = c.GetInStoreDirMkdir("containers", containerId, "rootfs")
	if err != nil {
		return
	}

	statePath, err = c.GetInStoreDirMkdir("states", containerId)
	if err != nil {
		return
	}

	return
}

// ExecInContainer uses nsenter to enter the pid namespace of the given
// ontainer and execute the given command.
func (c *Cpak) ExecInContainer(override types.Override, container types.Container, command []string) (err error) {
	pid, err := getPidFromEnvContainerId(container.Id)
	if err != nil {
		return
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())

	//nsenterBin := filepath.Join(c.Options.BinPath, "nsenter")
	// TODO: use from busybox
	nsenterBin := "nsenter"
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
		fmt.Sprintf("%d", pid),
		"--",
	}

	if !override.AsRoot {
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
	envVars = append(envVars, "CPAK_CONTAINER_ID="+container.Id)

	cmd := exec.Command(nsenterBin, cmds...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = envVars

	err = cmd.Run()
	if err != nil {
		return
	}

	return
}

// getPidFromEnvContainerId returns the pid of the process with the given containerId
// by looking at the environment variables of all the processes.
func getPidFromEnvContainerId(containerId string) (pid int, err error) {
	env := "CPAK_CONTAINER_ID=" + containerId
	pids, err := tools.GetPidFromEnv(env)
	if err != nil {
		return
	}

	if isVerbose {
		fmt.Println("Pids found:", pids)
	}

	if len(pids) == 0 {
		err = fmt.Errorf("no process with containerId %s found", containerId)
		return
	}

	return pids[0], nil
}

// CleanupContainer removes the container with the given id.
func (c *Cpak) CleanupContainer(container types.Container) (err error) {
	// we don't care about the error here, we just want to make sure that
	// the container filesystem is getting deleted
	os.RemoveAll(container.StatePath)
	os.RemoveAll(c.GetInStoreDir("containers", container.Id))
	os.RemoveAll(c.GetInStoreDir("states", container.Id))

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	err = store.RemoveContainer(container.Id)
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
		// first we check in the user's home directory
		cpakBinary = filepath.Join(os.Getenv("HOME"), ".local", "bin", "cpak")
		// if it is not there, we check in the system's bin directory using
		// the LookPath function
		if _, err = os.Stat(cpakBinary); os.IsNotExist(err) {
			cpakBinary, err = exec.LookPath("cpak")
			if err != nil {
				return
			}
		}
	}

	return
}

// getNested checks if the /tmp/.cpak file exists and returns the parent
// application id from it.
func getNested() (parentAppId string, nested bool) {
	nested = false
	parentAppId = ""
	if _, err := os.Stat("/tmp/.cpak"); err == nil {
		nested = true
		file, err := os.Open("/tmp/.cpak")
		if err != nil {
			return
		}
		defer file.Close()

		_, err = fmt.Fscanln(file, &parentAppId)
		if err != nil {
			return
		}
	}

	return
}
