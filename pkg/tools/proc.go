package tools

import (
	"fmt"
	"os/exec"
	"os/user"
	"strings"
)

// GetSubIDRanges returns the subuid and subgid ranges for the current user.
// It does so by calling the getsubids command.
func GetSubIDRanges() ([]string, []string, error) {
	// TODO: check if there are more efficient ways to do this, e.g.
	// by not relying on external commands
	user, err := user.Current()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting current user: %w", err)
	}

	subUIDout, err := exec.Command("getsubids", user.Username).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting subuids: %w", err)
	}

	subUIDSlice := strings.Split(
		strings.Trim(string(subUIDout), "\n"),
		" ")[2:]

	subGIDout, err := exec.Command("getsubids", "-g", user.Username).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting subgids: %w", err)
	}

	subGIDSlice := strings.Split(
		strings.Trim(string(subGIDout), "\n"),
		" ")[2:]

	subUIDSlice = append([]string{user.Uid}, subUIDSlice...)
	subGIDSlice = append([]string{user.Gid}, subGIDSlice...)

	return subUIDSlice, subGIDSlice, nil
}
