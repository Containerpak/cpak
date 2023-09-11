package cpak

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/google/go-containerregistry/pkg/legacy"
	"github.com/google/uuid"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/shirou/gopsutil/process"
)

// PrepareContainer prepares a container for the given application.
// It returns an existing one if it exists, otherwise it creates a new one.
func (c *Cpak) PrepareContainer(app types.Application) (container types.Container, err error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	containers, err := store.GetApplicationContainers(app)
	if err != nil {
		return
	}

	imagePath := filepath.Join(c.Options.StorePath, "images", app.Id)
	container.Application = app
	config := &legacy.LayerConfigFile{}
	err = json.Unmarshal([]byte(app.Config), config)
	if err != nil {
		return
	}

	if len(containers) > 0 {
		container = containers[0]
		container.StatePath = filepath.Join(c.Options.StorePath, "states", container.Id)
		if !c.IsContainerRunning(container.Pid) {
			err = c.CleanupContainer(container)
			if err != nil {
				return
			}
		} else {
			fmt.Println("Container already running, attaching to it:", container.Id)
			return
		}
	}

	container.Id, container.StatePath, err = c.CreateContainer()
	if err != nil {
		return
	}

	err = store.NewContainer(container)
	if err != nil {
		return
	}

	fmt.Println("Container created:", container.Id)

	container.Pid, err = c.StartContainer(container, config, imagePath)
	if err != nil {
		return
	}

	fmt.Println("Container prepared:", container.Id)
	return
}

func (c *Cpak) StartContainer(container types.Container, config *legacy.LayerConfigFile, imagePath string) (pid int, err error) {
	layers := ""
	for _, layer := range container.Application.Layers {
		layers += layer + ":"
	}

	rootlesskitBin := filepath.Join(c.Options.BinPath, "rootlesskit")
	cmds := []string{
		"--debug",
		//"--net=slirp4netns",
		"--mtu=1500",
		"--cgroupns=true",
		"--utsns=true",
		"--ipcns=true",
		"--copy-up=/etc",
		"--propagation=rslave",
		os.Args[0],
		"spawn",
	}
	cmds = append(cmds, "--container-id", container.Id)
	cmds = append(cmds, "--rootfs", filepath.Join(c.Options.StorePath, "containers", container.Id, "rootfs"))
	cmds = append(cmds, "--state-dir", container.StatePath)
	cmds = append(cmds, "--layers", layers)
	cmds = append(cmds, "--image-dir", imagePath)
	for _, env := range config.Config.Env {
		cmds = append(cmds, "--env", env)
	}

	// following is where dependencies and future dependencies are exported
	cmds = append(cmds, "--env", "PATH="+fmt.Sprintf("%s/%s", c.Options.ExportsPath, container.Application.Id)+":$PATH")

	cmd := exec.Command(rootlesskitBin, cmds...)
	fmt.Println(cmd.String())
	fmt.Println(cmd.Args)
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

	rootfs := filepath.Join(c.Options.StorePath, "containers", containerId, "rootfs")
	err = os.MkdirAll(rootfs, 0755)
	if err != nil {
		return
	}

	statePath = filepath.Join(c.Options.StorePath, "states", containerId)
	err = os.MkdirAll(statePath, 0755)
	if err != nil {
		return
	}

	return
}

// ExecInContainer uses nsenter to enter the pid namespace of the given
// ontainer and execute the given command.
func (c *Cpak) ExecInContainer(container types.Container, command []string) (err error) {
	pid, err := getPidFromEnvSpawn(container.Id)
	if err != nil {
		return
	}

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
		//"-S", strconv.FormatInt(int64(os.Getuid()), 10),
		//"-G", strconv.FormatInt(int64(os.Getgid()), 10),
		"-t",
		fmt.Sprintf("%d", pid),
		"--",
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

// IsContainerRunning returns true if the container with the given pid is
// running, false otherwise.
func (c *Cpak) IsContainerRunning(pid int) bool {
	proc, _ := os.FindProcess(pid)
	// On Unix systems, FindProcess always succeeds and returns a Process
	// for the given pid, regardless of whether the process exists.
	err := proc.Signal(syscall.SIGCONT)
	return err == nil
}

// CleanupContainer removes the container with the given id.
func (c *Cpak) CleanupContainer(container types.Container) (err error) {
	// we don't care about the error here, we just want to make sure that
	// the container filesystem is getting deleted
	os.RemoveAll(container.StatePath)
	os.RemoveAll(filepath.Join(c.Options.StorePath, "containers", container.Id))
	os.RemoveAll(filepath.Join(c.Options.StorePath, "states", container.Id))

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
