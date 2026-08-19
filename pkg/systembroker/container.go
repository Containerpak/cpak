/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

const containerOwnerLabel = "io.cpak.owner"

var containerResourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var containerEnvironmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.*$`)
var containerUserPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?::[A-Za-z0-9_.-]+)?$`)

// containerIdentifierPattern is how a backend spells a container identifier,
// full or shortened to the twelve characters it prints.
var containerIdentifierPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func executeContainer(ctx context.Context, owner string, capabilities map[string]bool, paths []ContainerPathGrant, request ContainerRequest, stdout, stderr io.Writer) (int, error) {
	arguments, owned, err := containerArguments(owner, capabilities, paths, request)
	if err != nil {
		return 0, err
	}
	binary, err := trustedContainerBinary(containerBackend(request))
	if err != nil {
		return 0, err
	}
	if owned {
		resolved, resolveErr := ownedContainerRequest(ctx, binary, owner, request)
		if resolveErr != nil {
			return 0, resolveErr
		}
		arguments, _, err = containerArguments(owner, capabilities, paths, resolved)
		if err != nil {
			return 0, err
		}
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Stdin = nil
	err = command.Run()
	if err == nil {
		return 0, nil
	}
	if ctx.Err() != nil {
		return 130, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	return 0, fmt.Errorf("container backend failed: %w", err)
}

func containerArguments(owner string, capabilities map[string]bool, paths []ContainerPathGrant, request ContainerRequest) ([]string, bool, error) {
	if err := validateContainerRequest(request); err != nil {
		return nil, false, err
	}
	arguments := []string{request.Operation}
	switch request.Operation {
	case "version", "info", "ps", "images", "inspect", "logs", "stats":
		if !capabilities[types.HostActionContainersRead] {
			return nil, false, errors.New("reading host containers is not permitted")
		}
		return append(arguments, containerReadArguments(request)...), false, nil
	case "run", "create":
		if !capabilities[types.HostActionContainersManageOwned] {
			return nil, false, errors.New("creating managed host containers is not permitted")
		}
		created, err := containerCreateArguments(owner, paths, request)
		if err != nil {
			return nil, false, err
		}
		return append(arguments, created...), false, nil
	case "start", "stop", "restart", "rm":
		if !capabilities[types.HostActionContainersManageOwned] {
			return nil, false, errors.New("managing host containers is not permitted")
		}
		return append(arguments, containerMutationArguments(request)...), true, nil
	case "exec":
		if !capabilities[types.HostActionContainersExecOwned] {
			return nil, false, errors.New("executing in managed host containers is not permitted")
		}
		return append(arguments, containerExecArguments(request)...), true, nil
	default:
		return nil, false, errors.New("unsupported container operation")
	}
}

// containerFormatJSON is the only value --format may carry.
//
// In podman and docker that flag is a Go template, so what arrived here as a
// string the caller chose was a template engine on the far side of the broker,
// reachable from inside a sandbox. Nothing was exploitable through it today,
// because a caller holding read can already run inspect and see the same
// fields, but a provider hands back data and does not take a program that says
// how to render it. A caller that wants a field parses the object.
const containerFormatJSON = "json"

func containerReadArguments(request ContainerRequest) []string {
	arguments := []string{}
	if request.All {
		arguments = append(arguments, "--all")
	}
	if request.NoTrunc {
		arguments = append(arguments, "--no-trunc")
	}
	if request.Follow {
		arguments = append(arguments, "--follow")
	}
	if request.Timestamps {
		arguments = append(arguments, "--timestamps")
	}
	if request.NoStream {
		arguments = append(arguments, "--no-stream")
	}
	if request.Tail != nil {
		arguments = append(arguments, "--tail", strconv.Itoa(*request.Tail))
	}
	if request.Format != "" {
		arguments = append(arguments, "--format", request.Format)
	}
	return append(arguments, request.Resources...)
}

func containerCreateArguments(owner string, paths []ContainerPathGrant, request ContainerRequest) ([]string, error) {
	arguments := []string{"--label", containerOwnerLabel + "=" + owner, "--label", "io.cpak.managed=true"}
	if request.Name != "" {
		arguments = append(arguments, "--name", request.Name)
	}
	if request.Remove {
		arguments = append(arguments, "--rm")
	}
	if request.Detach {
		arguments = append(arguments, "--detach")
	}
	if request.Workdir != "" {
		arguments = append(arguments, "--workdir", request.Workdir)
	}
	if request.User != "" {
		arguments = append(arguments, "--user", request.User)
	}
	if request.Entrypoint != "" {
		arguments = append(arguments, "--entrypoint", request.Entrypoint)
	}
	for _, environment := range request.Environment {
		arguments = append(arguments, "--env", environment)
	}
	for _, port := range request.Ports {
		arguments = append(arguments, "--publish", port)
	}
	for _, mount := range request.Mounts {
		source, err := validateContainerMount(mount, paths)
		if err != nil {
			return nil, err
		}
		value := source + ":" + mount.Target
		if mount.ReadOnly {
			value += ":ro"
		}
		arguments = append(arguments, "--volume", value)
	}
	arguments = append(arguments, request.Image)
	return append(arguments, request.Command...), nil
}

func containerMutationArguments(request ContainerRequest) []string {
	arguments := []string{}
	if request.Timeout > 0 {
		arguments = append(arguments, "--time", strconv.Itoa(request.Timeout))
	}
	if request.Force {
		arguments = append(arguments, "--force")
	}
	return append(arguments, request.Resources...)
}

func containerExecArguments(request ContainerRequest) []string {
	arguments := []string{}
	if request.Workdir != "" {
		arguments = append(arguments, "--workdir", request.Workdir)
	}
	if request.User != "" {
		arguments = append(arguments, "--user", request.User)
	}
	for _, environment := range request.Environment {
		arguments = append(arguments, "--env", environment)
	}
	arguments = append(arguments, request.Resources[0])
	return append(arguments, request.Command...)
}

func validateContainerRequest(request ContainerRequest) error {
	if request.Backend != "" && request.Backend != "podman" && request.Backend != "docker" {
		return errors.New("unsupported host container backend")
	}
	if len(request.Resources) > 32 || len(request.Command) > 128 || len(request.Environment) > 64 || len(request.Mounts) > 32 || len(request.Ports) > 32 {
		return errors.New("container request is too large")
	}
	for _, resource := range request.Resources {
		if !containerResourcePattern.MatchString(resource) {
			return errors.New("invalid container resource")
		}
	}
	for _, argument := range request.Command {
		if len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
			return errors.New("invalid container command")
		}
	}
	for _, environment := range request.Environment {
		if len(environment) > 4096 || !containerEnvironmentPattern.MatchString(environment) || strings.ContainsRune(environment, '\x00') {
			return errors.New("invalid container environment")
		}
	}
	if request.Format != "" && request.Format != containerFormatJSON {
		return errors.New("the only container output format is " + containerFormatJSON)
	}
	if request.Name != "" && !containerResourcePattern.MatchString(request.Name) {
		return errors.New("invalid container name")
	}
	// A name is matched before an identifier, so a name spelled like one
	// stands in front of another container: every stop, rm and exec its owner
	// issues by that identifier reaches this container instead.
	if containerIdentifierPattern.MatchString(request.Name) {
		return errors.New("container name is spelled like a container identifier")
	}
	if request.Workdir != "" && (!filepath.IsAbs(request.Workdir) || filepath.Clean(request.Workdir) != request.Workdir) {
		return errors.New("invalid container working directory")
	}
	if request.User != "" && !containerUserPattern.MatchString(request.User) {
		return errors.New("invalid container user")
	}
	if request.Entrypoint != "" && (len(request.Entrypoint) > 4096 || strings.ContainsRune(request.Entrypoint, '\x00')) {
		return errors.New("invalid container entrypoint")
	}
	for _, port := range request.Ports {
		if err := validateContainerPort(port); err != nil {
			return err
		}
	}
	if request.Image != "" {
		if _, err := oci.ParseReference(request.Image); err != nil {
			return errors.New("invalid container image")
		}
	}
	return validateContainerShape(request)
}

func validateContainerShape(request ContainerRequest) error {
	createFields := request.Image != "" || request.Name != "" || len(request.Mounts) > 0 || len(request.Ports) > 0 || request.Entrypoint != "" || request.Remove || request.Detach
	execFields := len(request.Command) > 0 || len(request.Environment) > 0 || request.Workdir != "" || request.User != ""
	readFields := request.All || request.Follow || request.NoTrunc || request.NoStream || request.Timestamps || request.Tail != nil || request.Format != ""
	mutationFields := request.Timeout != 0 || request.Force
	switch request.Operation {
	case "version", "info":
		if len(request.Resources) != 0 || createFields || execFields || mutationFields || request.All || request.Follow || request.NoTrunc || request.NoStream || request.Timestamps || request.Tail != nil {
			return errors.New("invalid container information request")
		}
	case "ps", "images":
		if len(request.Resources) != 0 || createFields || execFields || mutationFields || request.Follow || request.NoStream || request.Timestamps || request.Tail != nil {
			return errors.New("invalid container list request")
		}
	case "inspect":
		if len(request.Resources) == 0 || createFields || execFields || mutationFields || request.All || request.Follow || request.NoTrunc || request.NoStream || request.Timestamps || request.Tail != nil {
			return errors.New("invalid container inspect request")
		}
	case "logs":
		if len(request.Resources) != 1 || createFields || execFields || mutationFields || request.All || request.NoTrunc || request.NoStream || request.Format != "" {
			return errors.New("invalid container log request")
		}
	case "stats":
		if createFields || execFields || mutationFields || request.All || request.Follow || request.NoTrunc || request.Timestamps || request.Tail != nil {
			return errors.New("invalid container stats request")
		}
	case "run":
		if request.Image == "" || len(request.Resources) != 0 || readFields || mutationFields {
			return errors.New("invalid container run request")
		}
	case "create":
		if request.Image == "" || len(request.Resources) != 0 || readFields || mutationFields || request.Remove || request.Detach {
			return errors.New("invalid container create request")
		}
	case "start":
		if len(request.Resources) == 0 || createFields || execFields || readFields || mutationFields {
			return errors.New("invalid container start request")
		}
	case "stop", "restart":
		if len(request.Resources) == 0 || createFields || execFields || readFields || request.Force {
			return fmt.Errorf("invalid container %s request", request.Operation)
		}
	case "rm":
		if len(request.Resources) == 0 || createFields || execFields || readFields || request.Timeout != 0 {
			return errors.New("invalid container removal request")
		}
	case "exec":
		if len(request.Resources) != 1 || len(request.Command) == 0 || createFields || readFields || mutationFields {
			return errors.New("invalid container exec request")
		}
	default:
		return errors.New("unsupported container operation")
	}
	return nil
}

// validateContainerMount answers with the path the grant was checked against.
//
// The backend resolves the source a second time when it binds it, so a source
// the caller can still rewrite is a different directory there than it was
// here. The mount is issued against the resolved path instead.
func validateContainerMount(mount ContainerMount, grants []ContainerPathGrant) (string, error) {
	if !filepath.IsAbs(mount.Source) || !filepath.IsAbs(mount.Target) || filepath.Clean(mount.Source) != mount.Source || filepath.Clean(mount.Target) != mount.Target {
		return "", errors.New("invalid container mount")
	}
	// The argument is assembled as src:dst[:opts], so a colon in either half
	// is a field separator the caller wrote: a target of /data:z hands podman
	// the relabel option for a host path the broker never meant to expose.
	if strings.ContainsRune(mount.Source, ':') || strings.ContainsRune(mount.Target, ':') {
		return "", errors.New("container mount path carries a colon")
	}
	resolved, err := filepath.EvalSymlinks(mount.Source)
	if err != nil {
		return "", errors.New("container mount source is unavailable")
	}
	if strings.ContainsRune(resolved, ':') {
		return "", errors.New("container mount path carries a colon")
	}
	for _, grant := range grants {
		if !pathContains(grant.Path, resolved) {
			continue
		}
		if grant.ReadOnly && !mount.ReadOnly {
			return "", errors.New("container mount exceeds the granted filesystem access")
		}
		if err := confirmContainerMountSource(resolved); err != nil {
			return "", err
		}
		return resolved, nil
	}
	return "", errors.New("container mount is outside the granted filesystem paths")
}

// confirmContainerMountSource refuses a source whose last component became a
// symlink after it was resolved.
//
// It proves nothing about what the backend will find later, which is why the
// argument carries the resolved path rather than the one the caller sent; this
// only catches the swap that happened while the grant was being checked.
func confirmContainerMountSource(path string) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("container mount source is unavailable")
	}
	_ = unix.Close(fd)
	return nil
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	return parent == child || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func validateContainerPort(value string) error {
	protocol := ""
	if base, suffix, found := strings.Cut(value, "/"); found {
		value = base
		protocol = suffix
	}
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		return errors.New("invalid container port protocol")
	}
	parts := strings.Split(value, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return errors.New("invalid container port")
	}
	for _, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("invalid container port")
		}
	}
	return nil
}

func containerBackend(request ContainerRequest) string {
	if request.Backend == "" {
		return "podman"
	}
	return request.Backend
}

func trustedContainerBinary(backend string) (string, error) {
	if backend != "podman" && backend != "docker" {
		return "", errors.New("unsupported host container backend")
	}
	path, err := exec.LookPath(backend)
	if err != nil {
		return "", fmt.Errorf("host container backend is unavailable: %s", backend)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("host container backend is unavailable: %s", backend)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return "", errors.New("host container backend is not trusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return "", errors.New("host container backend is not trusted")
	}
	return path, nil
}

// ownedContainerRequest answers with the request the backend is asked to run.
//
// The backend matches a name before it matches an identifier, so the name that
// answered the ownership check is not necessarily the container the command
// reaches. Each name is turned into the identifier it named while it was
// checked, and the command is issued against that.
func ownedContainerRequest(ctx context.Context, binary, owner string, request ContainerRequest) (ContainerRequest, error) {
	resolved := request
	resolved.Resources = make([]string, 0, len(request.Resources))
	for _, resource := range request.Resources {
		identifier, err := requireOwnedContainer(ctx, binary, owner, resource)
		if err != nil {
			return ContainerRequest{}, err
		}
		resolved.Resources = append(resolved.Resources, identifier)
	}
	return resolved, nil
}

func requireOwnedContainer(ctx context.Context, binary, owner, resource string) (string, error) {
	command := exec.CommandContext(ctx, binary, "inspect", "--format", "{{ .Id }} {{ index .Config.Labels \""+containerOwnerLabel+"\" }}", resource)
	output, err := command.Output()
	if err != nil {
		return "", errors.New("managed host container was not found")
	}
	identifier, label, found := strings.Cut(strings.TrimSpace(string(output)), " ")
	if !found || strings.TrimSpace(label) != owner {
		return "", errors.New("host container is not owned by this cpak")
	}
	if !containerIdentifierPattern.MatchString(identifier) {
		return "", errors.New("managed host container was not found")
	}
	return identifier, nil
}
