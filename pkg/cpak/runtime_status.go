/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/appservice"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type RuntimeStatus struct {
	Package        string    `json:"package"`
	Version        string    `json:"version"`
	Origin         string    `json:"origin"`
	Instance       string    `json:"instance"`
	Service        string    `json:"service,omitempty"`
	ContainerID    string    `json:"container_id,omitempty"`
	ContainerPID   int       `json:"container_pid,omitempty"`
	ContainerState string    `json:"container"`
	ProcessState   string    `json:"process"`
	Health         string    `json:"health"`
	Since          time.Time `json:"since,omitempty"`
	Network        string    `json:"network"`
	Ports          []int     `json:"ports,omitempty"`
	Restarts       int       `json:"restarts,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
}

func (c *Cpak) RuntimeStatuses() ([]RuntimeStatus, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	apps, err := store.GetApplications()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	containers, err := store.GetContainers()
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	managed, err := c.managedServiceStatuses()
	if err != nil {
		return nil, err
	}
	applications := make(map[string]types.Application, len(apps))
	for _, app := range apps {
		applications[app.CpakId] = app
	}
	statuses := make([]RuntimeStatus, 0, len(containers)+len(managed))
	candidates := make(map[string][]int)
	seen := make(map[string]bool)
	for _, container := range containers {
		app, found := applicationForContainer(applications, container)
		if !found {
			continue
		}
		status := RuntimeStatus{
			Package: app.Name, Version: app.Version, Origin: app.Origin, Instance: container.Instance,
			ContainerID: container.CpakId, ContainerPID: container.Pid, ContainerState: "stopped",
			ProcessState: "stopped", Health: "none", Since: container.CreateTimestamp,
			Network: runtimeNetwork(app.ParsedOverride),
		}
		if containerProcessRunning(container) {
			status.ContainerState = "running"
			status.ProcessState = "idle"
			if containerHasApplicationProcess(container) {
				status.ProcessState = "running"
			}
			status.Ports = containerListeningPorts(container)
		}
		key := runtimeKey(app.Origin, container.Instance)
		if _, ok := managed[key]; ok {
			candidates[key] = append(candidates[key], len(statuses))
		}
		statuses = append(statuses, status)
	}
	for key, indexes := range candidates {
		selected := indexes[0]
		for _, index := range indexes[1:] {
			if preferRuntimeStatus(statuses[index], statuses[selected]) {
				selected = index
			}
		}
		applyManagedStatus(&statuses[selected], managed[key])
		seen[key] = true
	}
	for key, service := range managed {
		if seen[key] {
			continue
		}
		app := applicationForOrigin(apps, service.Origin)
		status := RuntimeStatus{
			Package: app.Name, Version: app.Version, Origin: service.Origin, Instance: service.Instance,
			ContainerState: "stopped", ProcessState: "stopped", Health: service.Health,
			Network: runtimeNetwork(app.ParsedOverride),
		}
		applyManagedStatus(&status, service)
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].Package != statuses[j].Package {
			return statuses[i].Package < statuses[j].Package
		}
		return statuses[i].Instance < statuses[j].Instance
	})
	return statuses, nil
}

func (c *Cpak) managedServiceStatuses() (map[string]appservice.Status, error) {
	result := make(map[string]appservice.Status)
	definitions, err := (appservice.Store{Directory: filepath.Join(c.Options.StorePath, "services")}).List()
	if err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		health := "none"
		if definition.HealthCommand != "" {
			health = "unknown"
		}
		result[runtimeKey(definition.Origin, definition.Instance())] = appservice.Status{
			Name: definition.Name, Origin: definition.Origin, Instance: definition.Instance(),
			Enabled: definition.Enabled, State: "stopped", Health: health,
		}
	}
	serviceSocket, err := HostServiceSocketPath()
	if err != nil {
		return result, nil
	}
	managerSocket := appservice.ManagerSocketPath(serviceSocket)
	ready, err := socketIsReady(managerSocket)
	if err != nil || !ready {
		return result, nil
	}
	response, err := appservice.Send(managerSocket, appservice.ControlRequest{Action: "status"}, time.Second)
	if err != nil {
		return result, nil
	}
	for _, status := range response.Services {
		result[runtimeKey(status.Origin, status.Instance)] = status
	}
	return result, nil
}

func preferRuntimeStatus(candidate, current RuntimeStatus) bool {
	if candidate.ContainerState != current.ContainerState {
		return candidate.ContainerState == "running"
	}
	return candidate.Since.After(current.Since)
}

func applyManagedStatus(status *RuntimeStatus, service appservice.Status) {
	status.Service = service.Name
	status.ProcessState = service.State
	status.Health = service.Health
	status.Restarts = service.Restarts
	status.LastError = service.LastError
	if !service.Since.IsZero() {
		status.Since = service.Since
	}
}

func applicationForContainer(apps map[string]types.Application, container types.Container) (types.Application, bool) {
	if app, found := apps[container.ApplicationCpakId]; found {
		return app, true
	}
	for id, app := range apps {
		if strings.HasPrefix(container.ApplicationCpakId, id+":instance:") {
			return app, true
		}
	}
	return types.Application{}, false
}

func applicationForOrigin(apps []types.Application, origin string) types.Application {
	for _, app := range apps {
		if app.Origin == origin {
			return app
		}
	}
	return types.Application{Origin: origin, Name: origin}
}

func runtimeKey(origin, instance string) string {
	return origin + "\x00" + instance
}

func runtimeNetwork(override types.Override) string {
	switch {
	case override.HostNetwork:
		return "host"
	case override.Network:
		return "isolated"
	default:
		return "none"
	}
}

func containerHasApplicationProcess(container types.Container) bool {
	pid, ok := verifiedContainerProcess(container)
	if !ok {
		return false
	}
	return len(processTree(pid)) > 1
}

func containerListeningPorts(container types.Container) []int {
	pid, ok := verifiedContainerProcess(container)
	if !ok {
		return nil
	}
	return listeningPortsForProcess(pid)
}

func listeningPortsForProcess(pid int) []int {
	inodes := processSocketInodes(processTree(pid))
	if len(inodes) == 0 {
		return nil
	}
	ports := make(map[int]bool)
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "net", name))
		if err != nil {
			continue
		}
		for _, port := range parseListeningPorts(data, inodes) {
			ports[port] = true
		}
	}
	result := make([]int, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	sort.Ints(result)
	return result
}

func processTree(root int) []int {
	queue := []int{root}
	seen := make(map[int]bool)
	result := make([]int, 0)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		result = append(result, pid)
		queue = append(queue, processChildren(pid)...)
	}
	return result
}

func processChildren(pid int) []int {
	tasks, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "task"))
	if err != nil {
		return nil
	}
	children := make([]int, 0)
	for _, task := range tasks {
		data, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "task", task.Name(), "children"))
		if readErr != nil {
			continue
		}
		for _, value := range strings.Fields(string(data)) {
			child, parseErr := strconv.Atoi(value)
			if parseErr == nil {
				children = append(children, child)
			}
		}
	}
	return children
}

func processSocketInodes(processes []int) map[string]bool {
	result := make(map[string]bool)
	for _, pid := range processes {
		entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			target, readErr := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
			if readErr != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			result[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	return result
}

func parseListeningPorts(data []byte, inodes map[string]bool) []int {
	ports := make(map[int]bool)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" || !inodes[fields[9]] {
			continue
		}
		_, encodedPort, found := strings.Cut(fields[1], ":")
		if !found {
			continue
		}
		port, err := strconv.ParseUint(encodedPort, 16, 16)
		if err == nil && port > 0 {
			ports[int(port)] = true
		}
	}
	result := make([]int, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	sort.Ints(result)
	return result
}

func FilterRuntimeStatuses(statuses []RuntimeStatus, origin, instance string) ([]RuntimeStatus, error) {
	filtered := make([]RuntimeStatus, 0)
	for _, status := range statuses {
		if origin != "" && status.Origin != origin {
			continue
		}
		if instance != "" && status.Instance != instance && status.Service != instance {
			continue
		}
		filtered = append(filtered, status)
	}
	if len(filtered) == 0 {
		label := origin
		if instance != "" {
			label += " instance " + instance
		}
		return nil, fmt.Errorf("no runtime found for %s", label)
	}
	return filtered, nil
}

func RuntimeHealthError(statuses []RuntimeStatus) error {
	for _, status := range statuses {
		if status.ProcessState != "running" || status.Health == "unhealthy" || status.Health == "starting" || status.Health == "unknown" {
			return &types.ExitError{Code: 1}
		}
	}
	return nil
}
