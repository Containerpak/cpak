/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type DoctorCheck struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Required  bool   `json:"required"`
	Detail    string `json:"detail"`
}

type DoctorReport struct {
	Ready  bool          `json:"ready"`
	Checks []DoctorCheck `json:"checks"`
}

func Doctor() DoctorReport {
	checks := []DoctorCheck{
		checkLinux(),
		checkUserNamespaces(),
		checkOverlay(),
		checkMountSetattr(),
		checkSeccomp(),
		checkLandlock(),
		checkCgroup(),
		checkInit(),
		checkDisplay(),
		checkAudio(),
		{Name: "host command bridge", Available: true, Required: true, Detail: "built into cpak and restricted by each application policy"},
	}
	ready := true
	for _, check := range checks {
		if check.Required && !check.Available {
			ready = false
		}
	}
	return DoctorReport{Ready: ready, Checks: checks}
}

func checkLinux() DoctorCheck {
	return DoctorCheck{Name: "Linux", Available: runtime.GOOS == "linux", Required: true, Detail: runtime.GOOS + "/" + runtime.GOARCH}
}

func checkUserNamespaces() DoctorCheck {
	truePath, err := exec.LookPath("true")
	if err != nil {
		return DoctorCheck{Name: "unprivileged user namespaces", Required: true, Detail: "true executable not found"}
	}
	cmd := nativeNamespaceCommand(truePath, nil, namespaceOptions{IsolateNetwork: true, IsolateCgroup: true})
	if err = cmd.Run(); err != nil {
		return DoctorCheck{Name: "unprivileged user namespaces", Required: true, Detail: err.Error()}
	}
	return DoctorCheck{Name: "unprivileged user namespaces", Available: true, Required: true, Detail: "user, mount, PID, IPC, UTS, network and cgroup namespaces can be created"}
}

func checkOverlay() DoctorCheck {
	mountBinary, err := exec.LookPath("mount")
	if err != nil {
		return DoctorCheck{Name: "rootless OverlayFS", Required: true, Detail: "mount executable not found"}
	}
	root, err := os.MkdirTemp("", "cpak-overlay-probe-")
	if err != nil {
		return DoctorCheck{Name: "rootless OverlayFS", Required: true, Detail: err.Error()}
	}
	defer os.RemoveAll(root)
	paths := []string{"lower", "upper", "work", "merged"}
	for _, name := range paths {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			return DoctorCheck{Name: "rootless OverlayFS", Required: true, Detail: err.Error()}
		}
	}
	options := strings.Join([]string{
		"lowerdir=" + filepath.Join(root, "lower"),
		"upperdir=" + filepath.Join(root, "upper"),
		"workdir=" + filepath.Join(root, "work"),
		"userxattr",
	}, ",")
	cmd := nativeNamespaceCommand(mountBinary, []string{"-t", "overlay", "overlay", "-o", options, filepath.Join(root, "merged")}, namespaceOptions{})
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = runErr.Error()
		}
		return DoctorCheck{Name: "rootless OverlayFS", Required: true, Detail: detail}
	}
	return DoctorCheck{Name: "rootless OverlayFS", Available: true, Required: true, Detail: "overlay mount with userxattr succeeded in a user namespace"}
}

func checkMountSetattr() DoctorCheck {
	err := unix.MountSetattr(-1, "", unix.AT_EMPTY_PATH, &unix.MountAttr{})
	available := !errors.Is(err, unix.ENOSYS)
	detail := "recursive read-only mounts use the compatibility path"
	if available {
		detail = "mount_setattr is available"
	}
	return DoctorCheck{Name: "mount_setattr", Available: available, Required: false, Detail: detail}
}

func checkSeccomp() DoctorCheck {
	action := uint32(unix.SECCOMP_RET_KILL_PROCESS)
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_GET_ACTION_AVAIL, 0, uintptr(unsafe.Pointer(&action)))
	available := errno == 0
	detail := "seccomp filter actions are available"
	if !available {
		detail = errno.Error()
	}
	return DoctorCheck{Name: "seccomp", Available: available, Required: false, Detail: detail}
}

func checkLandlock() DoctorCheck {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	available := errno == 0 && abi > 0
	detail := "Landlock ABI unavailable"
	if available {
		detail = "Landlock ABI " + strconv.FormatUint(uint64(abi), 10)
	} else if errno != 0 {
		detail = errno.Error()
	}
	return DoctorCheck{Name: "Landlock", Available: available, Required: false, Detail: detail}
}

func checkCgroup() DoctorCheck {
	if fsType, err := filesystemType("/sys/fs/cgroup"); err != nil || fsType != "cgroup2fs" {
		return DoctorCheck{Name: "cgroup v2 delegation", Required: false, Detail: "cgroup v2 is not mounted"}
	}
	cgroupPath, err := currentCgroupPath()
	if err != nil {
		return DoctorCheck{Name: "cgroup v2 delegation", Required: false, Detail: err.Error()}
	}
	available := unix.Access(cgroupPath, unix.W_OK) == nil
	detail := cgroupPath + " is not delegated to the current user"
	if available {
		detail = cgroupPath + " is writable"
	}
	return DoctorCheck{Name: "cgroup v2 delegation", Available: available, Required: false, Detail: detail}
}

func filesystemType(path string) (string, error) {
	var info syscall.Statfs_t
	if err := syscall.Statfs(path, &info); err != nil {
		return "", err
	}
	if info.Type == unix.CGROUP2_SUPER_MAGIC {
		return "cgroup2fs", nil
	}
	return fmt.Sprintf("0x%x", info.Type), nil
}

func currentCgroupPath() (string, error) {
	content, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative := strings.TrimPrefix(line, "0::")
			return filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+relative)), nil
		}
	}
	return "", errors.New("unified cgroup entry not found")
}

func checkInit() DoctorCheck {
	name := "unknown"
	if content, err := os.ReadFile("/proc/1/comm"); err == nil {
		name = strings.TrimSpace(string(content))
	}
	available := name != ""
	detail := name
	if name == "systemd" && !pathExists("/run/systemd/system") {
		detail = "systemd is PID 1 but its runtime interface is not exposed"
	}
	return DoctorCheck{Name: "host init", Available: available, Required: false, Detail: detail}
}

func checkDisplay() DoctorCheck {
	uid := strconv.Itoa(os.Getuid())
	wayland := os.Getenv("WAYLAND_DISPLAY")
	if wayland == "" {
		wayland = "wayland-0"
	}
	waylandPath := filepath.Join("/run/user", uid, wayland)
	x11 := pathExists("/tmp/.X11-unix") && os.Getenv("DISPLAY") != ""
	available := pathExists(waylandPath) || x11
	detail := "no Wayland or X11 display socket detected"
	if pathExists(waylandPath) {
		detail = "Wayland socket " + waylandPath
	} else if x11 {
		detail = "X11 display " + os.Getenv("DISPLAY")
	}
	return DoctorCheck{Name: "desktop display", Available: available, Required: false, Detail: detail}
}

func checkAudio() DoctorCheck {
	uid := strconv.Itoa(os.Getuid())
	pulse := filepath.Join("/run/user", uid, "pulse", "native")
	pipewire := filepath.Join("/run/user", uid, "pipewire-0")
	available := pathExists(pulse) || pathExists(pipewire)
	detail := "no PulseAudio or PipeWire socket detected"
	if pathExists(pipewire) {
		detail = "PipeWire socket " + pipewire
	} else if pathExists(pulse) {
		detail = "PulseAudio socket " + pulse
	}
	return DoctorCheck{Name: "desktop audio", Available: available, Required: false, Detail: detail}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
