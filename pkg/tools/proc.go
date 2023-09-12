package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"strings"
)

// GetSubIDRanges returns the subuid and subgid ranges for the current user.
func GetSubIDRanges() ([]string, []string, error) {
	user, err := user.Current()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting current user: %w", err)
	}

	subUIDSlice, err := readSubIDFile("/etc/subuid", user.Username)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting subuids: %w", err)
	}

	subGIDSlice, err := readSubIDFile("/etc/subgid", user.Username)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting subgids: %w", err)
	}

	subUIDSlice = append([]string{user.Uid}, subUIDSlice...)
	subGIDSlice = append([]string{user.Gid}, subGIDSlice...)

	return subUIDSlice, subGIDSlice, nil
}

// readSubIDFile reads the subuid or subgid file and returns the slice of subids
// for the given username.
func readSubIDFile(filename, username string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var subIDSlice []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 3 || parts[0] != username {
			continue
		}
		subIDSlice = append(subIDSlice, parts[2])
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return subIDSlice, nil
}
