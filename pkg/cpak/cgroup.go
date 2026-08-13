/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

func applyCgroupLimits(containerID string, pid int, override types.Override) (string, error) {
	if override.MemoryMaxMB == 0 && override.CPUQuota == 0 && override.PidsMax == 0 {
		return "", nil
	}
	if override.MemoryMaxMB < 0 || override.CPUQuota < 0 || override.CPUQuota > 1000 || override.PidsMax < 0 {
		return "", fmt.Errorf("invalid cgroup resource limits")
	}
	parent, err := currentCgroupPath()
	if err != nil {
		return "", fmt.Errorf("resource limits require cgroup v2: %w", err)
	}
	if unix.Access(parent, unix.W_OK) != nil {
		return "", fmt.Errorf("resource limits requested but cgroup %s is not delegated to the current user", parent)
	}
	controllers := requestedControllers(override)
	if err = enableCgroupControllers(parent, controllers); err != nil {
		return "", err
	}

	root := filepath.Join(parent, "cpak")
	if err = os.Mkdir(root, 0755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create cpak cgroup: %w", err)
	}
	if err = enableCgroupControllers(root, controllers); err != nil {
		return "", err
	}
	path := filepath.Join(root, safeCgroupName(containerID))
	if err = os.Mkdir(path, 0755); err != nil {
		return "", fmt.Errorf("create application cgroup: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(path)
		}
	}()

	if override.MemoryMaxMB > 0 {
		bytes := int64(override.MemoryMaxMB) * 1024 * 1024
		if err = writeCgroupValue(path, "memory.max", strconv.FormatInt(bytes, 10)); err != nil {
			return "", err
		}
	}
	if override.CPUQuota > 0 {
		quota := int64(override.CPUQuota) * 1000
		if err = writeCgroupValue(path, "cpu.max", fmt.Sprintf("%d 100000", quota)); err != nil {
			return "", err
		}
	}
	if override.PidsMax > 0 {
		if err = writeCgroupValue(path, "pids.max", strconv.Itoa(override.PidsMax)); err != nil {
			return "", err
		}
	}
	if err = writeCgroupValue(path, "cgroup.procs", strconv.Itoa(pid)); err != nil {
		return "", err
	}
	removeOnError = false
	return path, nil
}

func requestedControllers(override types.Override) []string {
	controllers := make([]string, 0, 3)
	if override.MemoryMaxMB > 0 {
		controllers = append(controllers, "memory")
	}
	if override.CPUQuota > 0 {
		controllers = append(controllers, "cpu")
	}
	if override.PidsMax > 0 {
		controllers = append(controllers, "pids")
	}
	return controllers
}

func enableCgroupControllers(path string, required []string) error {
	availableContent, err := os.ReadFile(filepath.Join(path, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read delegated cgroup controllers: %w", err)
	}
	available := make(map[string]bool)
	for _, controller := range strings.Fields(string(availableContent)) {
		available[controller] = true
	}
	for _, controller := range required {
		if !available[controller] {
			return fmt.Errorf("cgroup controller %s is not delegated", controller)
		}
	}
	if len(required) == 0 {
		return nil
	}
	values := make([]string, 0, len(required))
	for _, controller := range required {
		values = append(values, "+"+controller)
	}
	if err = os.WriteFile(filepath.Join(path, "cgroup.subtree_control"), []byte(strings.Join(values, " ")), 0600); err != nil {
		return fmt.Errorf("enable cgroup controllers in %s: %w", path, err)
	}
	return nil
}

func writeCgroupValue(path, name, value string) error {
	target := filepath.Join(path, name)
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cgroup controller for %s is not delegated", name)
		}
		return err
	}
	if err := os.WriteFile(target, []byte(value), 0600); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

func cleanupCgroup(containerID, storedPath string) {
	path := storedPath
	if path == "" {
		parent, err := currentCgroupPath()
		if err != nil {
			return
		}
		path = filepath.Join(parent, "cpak", safeCgroupName(containerID))
	}
	for attempts := 0; attempts < 20; attempts++ {
		removeErr := os.Remove(path)
		if removeErr == nil || os.IsNotExist(removeErr) {
			_ = os.Remove(filepath.Dir(path))
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func safeCgroupName(value string) string {
	value = strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			return char
		}
		return '_'
	}, value)
	if value == "" {
		return "application"
	}
	return value
}
