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
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/shirou/gopsutil/process"
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
		container.StatePath = c.GetInStoreDir("states", container.Id)
		// If the container is not running, we clean it up and create a new one
		// by escaping the if statement
		container.Pid, err = getPidFromEnvSpawn(container.Id)
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
	cpakBinary := os.Args[0]
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

	layersPath := c.GetInStoreDir("layers")
	rootfs = c.GetInStoreDir("containers", container.Id, "rootfs")
	overrideMounts := GetOverrideMounts(override)
	cmds := []string{
		"--debug", // TODO: move to a flag
		//"--net=slirp4netns",
		"--cgroupns=true",
		"--utsns=true",
		"--ipcns=true",
		"--copy-up=/etc",
		"--propagation=rslave",
		cpakBinary,
		"spawn",
	}
	cmds = append(cmds, "--container-id", container.Id)
	cmds = append(cmds, "--rootfs", rootfs)
	cmds = append(cmds, "--state-dir", container.StatePath)
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--layers-dir", layersPath)
	// exposing the host-spawn in xdg-open is needed for the browser to
	// be able to open the host's default browser, this is absolutely not
	// secure and should be changed in the future
	// TODO: help me dudo
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

	// following is where dependencies and future dependencies are exported
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
	pid = cmd.Process.Pid
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
	pid, err := getPidFromEnvSpawn(container.Id)
	if err != nil {
		return
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())

	//nsenterBin := filepath.Join(c.Options.BinPath, "nsenter") TODO: use from busybox
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

	cmd := exec.Command(nsenterBin, cmds...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	err = cmd.Run()
	if err != nil {
		return
	}

	return
}

// getPidFromEnvSpawn returns the pid of the process with the given containerId
// by looking at the environment variables of all the processes.
func getPidFromEnvSpawn(containerId string) (pid int, err error) {
	procs, err := process.Processes()
	if err != nil {
		return
	}

	for _, proc := range procs {
		var env []string
		env, err = proc.Environ()
		if err != nil {
			continue
		}

		for _, envVar := range env {
			if envVar == "CPAK_CONTAINER_ID="+containerId {
				pid = int(proc.Pid)
				return
			}
		}

	}

	return
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
