package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// GetSubIDRanges returns the subuid and subgid ranges for the current user.
func GetSubIDRanges() (subUIDSlice []string, subGIDSlice []string, err error) {
	var curUser *user.User
	curUser, err = user.Current()
	if err != nil {
		return
	}

	if _, err = os.Stat("/etc/subuid"); err == nil {
		return GetSubIDRangesNative(curUser)
	}
	return GetSubIDRangesCmd(curUser)
}

// GetSubIDRangesNative returns the subuid and subgid ranges for the current
// user by reading the /etc/subuid and /etc/subgid files.
func GetSubIDRangesNative(curUser *user.User) (subUIDSlice []string, subGIDSlice []string, err error) {
	subUIDSlice, err = readSubIDFile("/etc/subuid", curUser.Username)
	if err != nil {
		return
	}

	subGIDSlice, err = readSubIDFile("/etc/subgid", curUser.Username)
	if err != nil {
		return
	}

	subUIDSlice = append([]string{curUser.Uid}, subUIDSlice...)
	subGIDSlice = append([]string{curUser.Gid}, subGIDSlice...)

	return subUIDSlice, subGIDSlice, nil
}

// GetSubIDRangesCmd returns the subuid and subgid ranges for the current user
// by running the getsubids command.
func GetSubIDRangesCmd(curUser *user.User) (subUIDSlice []string, subGIDSlice []string, err error) {
	var subUIDout, subGIDout []byte
	subUIDout, err = exec.Command("getsubids", curUser.Username).Output()
	if err != nil {
		return
	}

	subUIDSlice = strings.Split(
		strings.Trim(string(subUIDout), "\n"),
		" ")[2:]

	subGIDout, err = exec.Command("getsubids", "-g", curUser.Username).Output()
	if err != nil {
		return
	}

	subGIDSlice = strings.Split(
		strings.Trim(string(subGIDout), "\n"),
		" ")[2:]

	subUIDSlice = append([]string{curUser.Uid}, subUIDSlice...)
	subGIDSlice = append([]string{curUser.Gid}, subGIDSlice...)

	return
}

// readSubIDFile reads the subuid or subgid file and returns the slice of subids
// for the given username.
func readSubIDFile(filename, username string) (subIDSlice []string, err error) {
	var file *os.File
	file, err = os.Open(filename)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 3 || parts[0] != username {
			continue
		}
		subIDSlice = append(subIDSlice, parts[2])
	}

	if err = scanner.Err(); err != nil {
		return
	}

	return
}

func GetPidFromEnv(envVar string) (int, error) {
	// Scan /proc for numeric directories
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("failed to read /proc: %w", err)
	}

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue // not a PID dir
		}

		envPath := filepath.Join("/proc", d.Name(), "environ")
		data, err := os.ReadFile(envPath)
		if err != nil {
			continue
		}

		// environ entries are '\x00'-separated
		envEntries := strings.Split(string(data), "\x00")
		for _, e := range envEntries {
			if e == envVar {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no process with env var %s found", envVar)
}
