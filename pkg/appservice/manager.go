/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
)

const managerSocketName = "service-manager.sock"

type ControlRequest struct {
	Action string `json:"action"`
	Name   string `json:"name,omitempty"`
}

type Status struct {
	Name      string    `json:"name"`
	Origin    string    `json:"origin"`
	Instance  string    `json:"instance"`
	Enabled   bool      `json:"enabled"`
	State     string    `json:"state"`
	Health    string    `json:"health"`
	Since     time.Time `json:"since,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"last_error,omitempty"`
}

type ControlResponse struct {
	Services []Status `json:"services,omitempty"`
	Error    string   `json:"error,omitempty"`
}

func ManagerSocketPath(serviceSocket string) string {
	return filepath.Join(filepath.Dir(serviceSocket), managerSocketName)
}

func Send(socketPath string, request ControlRequest, timeout time.Duration) (ControlResponse, error) {
	connection, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return ControlResponse{}, err
	}
	defer connection.Close()
	if deadlineErr := connection.SetDeadline(time.Now().Add(timeout)); deadlineErr != nil {
		return ControlResponse{}, deadlineErr
	}
	if err = json.NewEncoder(connection).Encode(request); err != nil {
		return ControlResponse{}, err
	}
	var response ControlResponse
	if err = json.NewDecoder(io.LimitReader(connection, 1024*1024)).Decode(&response); err != nil {
		return ControlResponse{}, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

type Manager struct {
	Store           Store
	Binary          string
	SocketPath      string
	RestartDelay    time.Duration
	RestartLimit    time.Duration
	StableAfter     time.Duration
	StopTimeout     time.Duration
	RefreshInterval time.Duration
}

type runtimeState struct {
	definition      Definition
	active          Definition
	target          bool
	removed         bool
	command         *exec.Cmd
	generation      uint64
	ready           bool
	stopping        bool
	stopReason      string
	restartBlocked  bool
	restartAt       time.Time
	restarts        int
	startedAt       time.Time
	healthFailures  int
	healthScheduled bool
	healthRunning   bool
	lastError       string
}

type processExit struct {
	name       string
	generation uint64
	err        error
}

type healthTick struct {
	name       string
	generation uint64
}

type healthResult struct {
	name       string
	generation uint64
	err        error
}

type controlCall struct {
	request  ControlRequest
	response chan ControlResponse
}

type runtimeManager struct {
	options       Manager
	states        map[string]*runtimeState
	order         []string
	exits         chan processExit
	healthTicks   chan healthTick
	healthResults chan healthResult
}

func (m Manager) Run(ctx context.Context) error {
	if !filepath.IsAbs(m.Binary) || !filepath.IsAbs(m.SocketPath) {
		return errors.New("service manager paths must be absolute")
	}
	m.defaults()
	listener, err := listen(m.SocketPath)
	if errors.Is(err, errManagerRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(m.SocketPath)

	calls := make(chan controlCall)
	go acceptControl(ctx, listener, calls)
	runtime := runtimeManager{
		options:       m,
		states:        make(map[string]*runtimeState),
		exits:         make(chan processExit, 32),
		healthTicks:   make(chan healthTick, 32),
		healthResults: make(chan healthResult, 32),
	}
	if err = runtime.reload(); err != nil {
		return err
	}
	runtime.reconcile(ctx)
	ticker := time.NewTicker(m.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case call := <-calls:
			response := runtime.control(ctx, call.request)
			runtime.reconcile(ctx)
			if len(response.Services) == 0 && response.Error == "" {
				response.Services = runtime.statuses()
			}
			call.response <- response
		case event := <-runtime.exits:
			runtime.processExited(event)
			runtime.reconcile(ctx)
		case event := <-runtime.healthTicks:
			runtime.runHealth(ctx, event)
		case event := <-runtime.healthResults:
			runtime.healthChecked(ctx, event)
			runtime.reconcile(ctx)
		case <-ticker.C:
			if err = runtime.reload(); err != nil {
				logger.Printf("Cannot reload cpak services: %v", err)
			}
			runtime.reconcile(ctx)
		case <-ctx.Done():
			return runtime.shutdown()
		}
	}
}

func (m *Manager) defaults() {
	if m.RestartDelay <= 0 {
		m.RestartDelay = time.Second
	}
	if m.RestartLimit <= 0 {
		m.RestartLimit = 30 * time.Second
	}
	if m.StableAfter <= 0 {
		m.StableAfter = time.Minute
	}
	if m.StopTimeout <= 0 {
		m.StopTimeout = 5 * time.Second
	}
	if m.RefreshInterval <= 0 {
		m.RefreshInterval = time.Second
	}
}

var errManagerRunning = errors.New("service manager is already running")

func listen(path string) (net.Listener, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create service runtime directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("service runtime path is not a directory: %s", directory)
	}
	if info.Mode().Perm()&0077 != 0 {
		if err = os.Chmod(directory, 0700); err != nil {
			return nil, fmt.Errorf("secure service runtime directory: %w", err)
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("service manager path %s is not a socket", path)
		}
		connection, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond)
		if dialErr == nil {
			connection.Close()
			return nil, errManagerRunning
		}
		if err = os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale service manager socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect service manager socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen for service manager requests: %w", err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("secure service manager socket: %w", err)
	}
	return listener, nil
}

func acceptControl(ctx context.Context, listener net.Listener, calls chan<- controlCall) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go handleControlConnection(ctx, connection, calls)
	}
}

func handleControlConnection(ctx context.Context, connection net.Conn, calls chan<- controlCall) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(io.LimitReader(connection, 64*1024))
	decoder.DisallowUnknownFields()
	var request ControlRequest
	if err := decoder.Decode(&request); err != nil {
		return
	}
	call := controlCall{request: request, response: make(chan ControlResponse, 1)}
	select {
	case calls <- call:
	case <-ctx.Done():
		return
	}
	select {
	case response := <-call.response:
		_ = json.NewEncoder(connection).Encode(response)
	case <-ctx.Done():
	}
}

func (m *runtimeManager) reload() error {
	definitions, err := m.options.Store.List()
	if err != nil {
		return err
	}
	order, err := Order(definitions)
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		found[definition.Name] = true
		state, exists := m.states[definition.Name]
		if !exists {
			m.states[definition.Name] = &runtimeState{definition: definition, target: definition.Enabled}
			continue
		}
		changed := !reflect.DeepEqual(state.definition, definition)
		if state.definition.Enabled != definition.Enabled || changed {
			state.target = definition.Enabled
			state.restartBlocked = false
			state.restartAt = time.Time{}
			state.restarts = 0
			state.lastError = ""
		}
		state.definition = definition
		state.removed = false
		if changed && state.command != nil && !state.stopping {
			state.restartBlocked = false
			m.stop(state, "reload")
		}
	}
	for name, state := range m.states {
		if found[name] {
			continue
		}
		state.target = false
		state.removed = true
		if state.command != nil && !state.stopping {
			m.stop(state, "removed")
		}
		if state.command == nil {
			delete(m.states, name)
		}
	}
	m.order = order
	return nil
}

func (m *runtimeManager) control(ctx context.Context, request ControlRequest) ControlResponse {
	switch request.Action {
	case "reload":
		if err := m.reload(); err != nil {
			return ControlResponse{Error: err.Error()}
		}
	case "start", "stop", "restart":
		if !validName(request.Name) {
			return ControlResponse{Error: fmt.Sprintf("invalid service name %q", request.Name)}
		}
		state, exists := m.states[request.Name]
		if !exists || state.removed {
			return ControlResponse{Error: fmt.Sprintf("service %s is not registered", request.Name)}
		}
		switch request.Action {
		case "start":
			state.target = true
			state.restartBlocked = false
			state.restartAt = time.Time{}
		case "stop":
			state.target = false
			state.restartBlocked = false
			if state.command != nil && !state.stopping {
				m.stop(state, "manual")
			}
		case "restart":
			state.target = true
			state.restartBlocked = false
			state.restartAt = time.Time{}
			if state.command != nil && !state.stopping {
				m.stop(state, "restart")
			}
		}
	case "status":
		return ControlResponse{Services: m.statuses()}
	default:
		return ControlResponse{Error: fmt.Sprintf("unsupported service action %q", request.Action)}
	}
	m.reconcile(ctx)
	return ControlResponse{Services: m.statuses()}
}

func (m *runtimeManager) reconcile(ctx context.Context) {
	for index := len(m.order) - 1; index >= 0; index-- {
		state := m.states[m.order[index]]
		if state == nil || state.command == nil || state.stopping {
			continue
		}
		if !state.target {
			m.stop(state, "manual")
			continue
		}
		if !m.dependenciesReady(state.definition) {
			m.stop(state, "dependency")
		}
	}
	for _, name := range m.order {
		state := m.states[name]
		if state == nil || !state.target || state.command != nil || state.stopping || state.restartBlocked {
			continue
		}
		if !state.restartAt.IsZero() && time.Now().Before(state.restartAt) {
			continue
		}
		if !m.dependenciesReady(state.definition) {
			continue
		}
		m.start(ctx, state)
	}
}

func (m *runtimeManager) dependenciesReady(definition Definition) bool {
	for _, dependency := range definition.DependsOn {
		state := m.states[dependency]
		if state == nil || !state.ready {
			return false
		}
	}
	return true
}

func (m *runtimeManager) start(ctx context.Context, state *runtimeState) {
	arguments := runArguments(state.definition)
	command := exec.Command(m.options.Binary, arguments...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logFile, err := os.OpenFile(filepath.Join(m.options.Store.Directory, state.definition.Name+".manager.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		m.startFailed(state, err)
		return
	}
	command.Stdout = logFile
	command.Stderr = logFile
	err = command.Start()
	_ = logFile.Close()
	if err != nil {
		m.startFailed(state, err)
		return
	}
	state.command = command
	state.active = state.definition
	state.generation++
	state.ready = state.definition.HealthCommand == ""
	state.stopping = false
	state.stopReason = ""
	state.restartAt = time.Time{}
	state.startedAt = time.Now()
	state.healthFailures = 0
	state.healthScheduled = false
	state.healthRunning = false
	state.lastError = ""
	generation := state.generation
	name := state.definition.Name
	go func() { m.exits <- processExit{name: name, generation: generation, err: command.Wait()} }()
	if state.definition.HealthCommand != "" {
		m.scheduleHealth(ctx, state, time.Duration(state.definition.HealthDelay)*time.Second)
	}
}

func (m *runtimeManager) startFailed(state *runtimeState, err error) {
	state.lastError = err.Error()
	state.ready = false
	if state.definition.Restart == RestartNever {
		state.restartBlocked = true
		return
	}
	m.scheduleRestart(state)
}

func (m *runtimeManager) processExited(event processExit) {
	state := m.states[event.name]
	if state == nil || state.generation != event.generation {
		return
	}
	reason := state.stopReason
	state.command = nil
	state.active = Definition{}
	state.ready = false
	state.stopping = false
	state.stopReason = ""
	state.healthScheduled = false
	state.healthRunning = false
	if time.Since(state.startedAt) >= m.options.StableAfter {
		state.restarts = 0
	}
	if state.removed {
		delete(m.states, event.name)
		return
	}
	if !state.target || reason == "manual" || reason == "removed" {
		return
	}
	if reason == "reload" || reason == "restart" || reason == "dependency" {
		state.restartAt = time.Time{}
		return
	}
	failed := event.err != nil || reason == "health"
	if event.err != nil {
		state.lastError = event.err.Error()
	}
	switch state.definition.Restart {
	case RestartAlways:
		m.scheduleRestart(state)
	case RestartOnFailure:
		if failed {
			m.scheduleRestart(state)
		} else {
			state.restartBlocked = true
		}
	case RestartNever:
		state.restartBlocked = true
	}
}

func (m *runtimeManager) scheduleRestart(state *runtimeState) {
	state.restarts++
	delay := m.options.RestartDelay
	for attempt := 1; attempt < state.restarts && delay < m.options.RestartLimit; attempt++ {
		delay *= 2
		if delay > m.options.RestartLimit {
			delay = m.options.RestartLimit
		}
	}
	state.restartAt = time.Now().Add(delay)
}

func (m *runtimeManager) stop(state *runtimeState, reason string) {
	if state.command == nil || state.stopping {
		return
	}
	state.stopping = true
	state.ready = false
	state.stopReason = reason
	definition := state.active
	if definition.Name == "" {
		definition = state.definition
	}
	command := state.command
	go m.terminate(definition, command)
}

func (m *runtimeManager) terminate(definition Definition, command *exec.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), m.options.StopTimeout)
	stop := exec.CommandContext(ctx, m.options.Binary, stopArguments(definition)...)
	stop.Stdout = io.Discard
	stop.Stderr = io.Discard
	_ = stop.Run()
	cancel()
	if command.Process == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	time.Sleep(m.options.StopTimeout)
	_ = command.Process.Kill()
}

func (m *runtimeManager) scheduleHealth(ctx context.Context, state *runtimeState, delay time.Duration) {
	if state.healthScheduled || state.command == nil {
		return
	}
	state.healthScheduled = true
	event := healthTick{name: state.definition.Name, generation: state.generation}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			m.healthTicks <- event
		case <-ctx.Done():
		}
	}()
}

func (m *runtimeManager) runHealth(ctx context.Context, event healthTick) {
	state := m.states[event.name]
	if state == nil || state.generation != event.generation || state.command == nil || state.stopping {
		return
	}
	state.healthScheduled = false
	if state.healthRunning {
		return
	}
	state.healthRunning = true
	definition := state.definition
	timeout := time.Duration(definition.HealthTimeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	go func() {
		healthCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		command := exec.CommandContext(healthCtx, m.options.Binary, healthArguments(definition)...)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		m.healthResults <- healthResult{name: definition.Name, generation: event.generation, err: command.Run()}
	}()
}

func (m *runtimeManager) healthChecked(ctx context.Context, event healthResult) {
	state := m.states[event.name]
	if state == nil || state.generation != event.generation || state.command == nil || state.stopping {
		return
	}
	state.healthRunning = false
	if event.err != nil {
		state.healthFailures++
		state.lastError = "health check: " + event.err.Error()
		if state.healthFailures <= state.definition.HealthRetries {
			m.scheduleHealth(ctx, state, time.Second)
			return
		}
		m.stop(state, "health")
		return
	}
	state.healthFailures = 0
	state.ready = true
	state.lastError = ""
	if state.definition.HealthInterval > 0 {
		m.scheduleHealth(ctx, state, time.Duration(state.definition.HealthInterval)*time.Second)
	}
}

func (m *runtimeManager) statuses() []Status {
	statuses := make([]Status, 0, len(m.states))
	for _, state := range m.states {
		status := Status{
			Name: state.definition.Name, Origin: state.definition.Origin,
			Instance: state.definition.Instance(), Enabled: state.definition.Enabled,
			Restarts: state.restarts, LastError: state.lastError,
		}
		switch {
		case state.stopping:
			status.State = "stopping"
		case state.command != nil && state.ready:
			status.State = "running"
			status.PID = state.command.Process.Pid
			status.Since = state.startedAt
		case state.command != nil:
			status.State = "starting"
			status.PID = state.command.Process.Pid
			status.Since = state.startedAt
		case !state.target:
			status.State = "stopped"
		case state.restartBlocked:
			status.State = "exited"
		case !m.dependenciesReady(state.definition):
			status.State = "waiting"
		case !state.restartAt.IsZero() && time.Now().Before(state.restartAt):
			status.State = "backoff"
		default:
			status.State = "starting"
		}
		switch {
		case state.definition.HealthCommand == "":
			status.Health = "none"
		case state.command != nil && state.ready:
			status.Health = "healthy"
		case state.healthFailures > 0:
			status.Health = "unhealthy"
		case state.command != nil:
			status.Health = "starting"
		default:
			status.Health = "unknown"
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func (m *runtimeManager) shutdown() error {
	for index := len(m.order) - 1; index >= 0; index-- {
		state := m.states[m.order[index]]
		if state == nil {
			continue
		}
		state.target = false
		if state.command != nil && !state.stopping {
			m.stop(state, "manual")
		}
	}
	deadline := time.NewTimer(m.options.StopTimeout * 2)
	defer deadline.Stop()
	for m.running() > 0 {
		select {
		case event := <-m.exits:
			m.processExited(event)
		case <-deadline.C:
			return errors.New("cpak services did not stop before the deadline")
		}
	}
	return nil
}

func (m *runtimeManager) running() int {
	running := 0
	for _, state := range m.states {
		if state.command != nil {
			running++
		}
	}
	return running
}

func runArguments(definition Definition) []string {
	arguments := []string{"run", "--instance", definition.Instance()}
	arguments = appendSelector(arguments, definition)
	if definition.ManifestService != "" {
		arguments = append(arguments, "--service", definition.ManifestService)
	}
	for _, entry := range definition.Environment {
		arguments = append(arguments, "--env", entry)
	}
	for _, path := range definition.EnvironmentFiles {
		arguments = append(arguments, "--env-file", path)
	}
	for _, secret := range definition.Secrets {
		arguments = append(arguments, "--secret", secret)
	}
	arguments = append(arguments, definition.Origin)
	if definition.Binary != "" {
		arguments = append(arguments, "--", definition.Binary)
		arguments = append(arguments, definition.Arguments...)
	}
	return arguments
}

func healthArguments(definition Definition) []string {
	arguments := []string{"run", "--instance", definition.Instance()}
	arguments = appendSelector(arguments, definition)
	arguments = appendRuntimeArguments(arguments, definition)
	return append(arguments, definition.Origin, "--", "@sh", "-c", definition.HealthCommand)
}

func appendRuntimeArguments(arguments []string, definition Definition) []string {
	for _, entry := range definition.Environment {
		arguments = append(arguments, "--env", entry)
	}
	for _, path := range definition.EnvironmentFiles {
		arguments = append(arguments, "--env-file", path)
	}
	for _, secret := range definition.Secrets {
		arguments = append(arguments, "--secret", secret)
	}
	return arguments
}

func stopArguments(definition Definition) []string {
	arguments := []string{"stop", "--instance", definition.Instance()}
	arguments = appendSelector(arguments, definition)
	return append(arguments, definition.Origin)
}

func appendSelector(arguments []string, definition Definition) []string {
	switch {
	case definition.Branch != "":
		return append(arguments, "--branch", definition.Branch)
	case definition.Commit != "":
		return append(arguments, "--commit", definition.Commit)
	case definition.Release != "":
		return append(arguments, "--release", definition.Release)
	default:
		return arguments
	}
}

func ReadManagerLog(store Store, name string, lines int) ([]string, error) {
	if !validName(name) {
		return nil, fmt.Errorf("invalid service name %q", name)
	}
	file, err := os.Open(filepath.Join(store.Directory, name+".manager.log"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	all := make([]string, 0, lines)
	for scanner.Scan() {
		all = append(all, scanner.Text())
		if len(all) > lines {
			all = all[1:]
		}
	}
	return all, scanner.Err()
}
