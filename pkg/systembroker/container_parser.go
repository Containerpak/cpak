/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func parseContainerCommand(args []string) (ContainerRequest, error) {
	if len(args) == 0 {
		return ContainerRequest{}, errors.New("container command is required")
	}
	if args[0] == "container" && len(args) > 1 {
		args = args[1:]
	}
	if args[0] == "image" && len(args) > 1 && args[1] == "ls" {
		args = append([]string{"images"}, args[2:]...)
	}
	operation := args[0]
	if operation == "ls" {
		operation = "ps"
	}
	request := ContainerRequest{Operation: operation}
	switch operation {
	case "version", "info", "ps", "images":
		return parseContainerList(request, args[1:])
	case "inspect":
		return parseContainerInspect(request, args[1:])
	case "logs":
		return parseContainerLogs(request, args[1:])
	case "stats":
		return parseContainerStats(request, args[1:])
	case "start", "stop", "restart", "rm":
		return parseContainerMutation(request, args[1:])
	case "exec":
		return parseContainerExec(request, args[1:])
	case "run", "create":
		return parseContainerCreate(request, args[1:])
	default:
		return ContainerRequest{}, fmt.Errorf("unsupported container operation: %s", operation)
	}
}

func parseContainerList(request ContainerRequest, args []string) (ContainerRequest, error) {
	if request.Operation == "version" || request.Operation == "info" {
		if len(args) == 0 {
			return request, nil
		}
	}
	for index := 0; index < len(args); index++ {
		name, inline, hasInline := strings.Cut(args[index], "=")
		switch name {
		case "-a", "--all":
			request.All = true
		case "--no-trunc":
			request.NoTrunc = true
		case "--format":
			value, next, err := containerOptionValue(args, index, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Format = value
			index = next
		default:
			return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
		}
	}
	return request, nil
}

func parseContainerInspect(request ContainerRequest, args []string) (ContainerRequest, error) {
	for index := 0; index < len(args); index++ {
		name, inline, hasInline := strings.Cut(args[index], "=")
		if name == "--format" || name == "-f" {
			value, next, err := containerOptionValue(args, index, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Format = value
			index = next
			continue
		}
		if strings.HasPrefix(args[index], "-") {
			return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
		}
		request.Resources = append(request.Resources, args[index])
	}
	if len(request.Resources) == 0 {
		return ContainerRequest{}, errors.New("container inspect requires a resource")
	}
	return request, nil
}

func parseContainerLogs(request ContainerRequest, args []string) (ContainerRequest, error) {
	for index := 0; index < len(args); index++ {
		name, inline, hasInline := strings.Cut(args[index], "=")
		switch name {
		case "-f", "--follow":
			request.Follow = true
		case "-t", "--timestamps":
			request.Timestamps = true
		case "--tail":
			value, next, err := containerOptionValue(args, index, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			tail, err := strconv.Atoi(value)
			if err != nil || tail < 0 {
				return ContainerRequest{}, errors.New("invalid container log tail")
			}
			request.Tail = &tail
			index = next
		default:
			if strings.HasPrefix(args[index], "-") {
				return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
			}
			request.Resources = append(request.Resources, args[index])
		}
	}
	if len(request.Resources) != 1 {
		return ContainerRequest{}, errors.New("container logs requires one resource")
	}
	return request, nil
}

func parseContainerStats(request ContainerRequest, args []string) (ContainerRequest, error) {
	for index := 0; index < len(args); index++ {
		name, inline, hasInline := strings.Cut(args[index], "=")
		switch name {
		case "--no-stream":
			request.NoStream = true
		case "--format":
			value, next, err := containerOptionValue(args, index, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Format = value
			index = next
		default:
			if strings.HasPrefix(args[index], "-") {
				return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
			}
			request.Resources = append(request.Resources, args[index])
		}
	}
	return request, nil
}

func parseContainerMutation(request ContainerRequest, args []string) (ContainerRequest, error) {
	for index := 0; index < len(args); index++ {
		name, inline, hasInline := strings.Cut(args[index], "=")
		switch name {
		case "-t", "--time":
			value, next, err := containerOptionValue(args, index, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Timeout, err = strconv.Atoi(value)
			if err != nil || request.Timeout < 0 || request.Timeout > 300 {
				return ContainerRequest{}, errors.New("invalid container timeout")
			}
			index = next
		case "-f", "--force":
			if request.Operation != "rm" {
				return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
			}
			request.Force = true
		default:
			if strings.HasPrefix(args[index], "-") {
				return ContainerRequest{}, fmt.Errorf("unsupported container option: %s", name)
			}
			request.Resources = append(request.Resources, args[index])
		}
	}
	if len(request.Resources) == 0 {
		return ContainerRequest{}, fmt.Errorf("container %s requires a resource", request.Operation)
	}
	return request, nil
}

func parseContainerExec(request ContainerRequest, args []string) (ContainerRequest, error) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		name, inline, hasInline := strings.Cut(args[0], "=")
		switch name {
		case "-w", "--workdir":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Workdir = value
			args = args[next+1:]
		case "-u", "--user":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.User = value
			args = args[next+1:]
		case "-e", "--env":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Environment = append(request.Environment, value)
			args = args[next+1:]
		default:
			return ContainerRequest{}, fmt.Errorf("unsupported container exec option: %s", name)
		}
	}
	if len(args) < 2 {
		return ContainerRequest{}, errors.New("container exec requires a resource and command")
	}
	request.Resources = []string{args[0]}
	request.Command = append([]string{}, args[1:]...)
	return request, nil
}

func parseContainerCreate(request ContainerRequest, args []string) (ContainerRequest, error) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		name, inline, hasInline := strings.Cut(args[0], "=")
		switch name {
		case "--name":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Name = value
			args = args[next+1:]
		case "-e", "--env":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Environment = append(request.Environment, value)
			args = args[next+1:]
		case "-v", "--volume":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			mount, err := parseContainerMount(value)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Mounts = append(request.Mounts, mount)
			args = args[next+1:]
		case "-p", "--publish":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Ports = append(request.Ports, value)
			args = args[next+1:]
		case "-w", "--workdir":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Workdir = value
			args = args[next+1:]
		case "-u", "--user":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.User = value
			args = args[next+1:]
		case "--entrypoint":
			value, next, err := containerOptionValue(args, 0, inline, hasInline)
			if err != nil {
				return ContainerRequest{}, err
			}
			request.Entrypoint = value
			args = args[next+1:]
		case "--rm":
			request.Remove = true
			args = args[1:]
		case "-d", "--detach":
			request.Detach = true
			args = args[1:]
		default:
			return ContainerRequest{}, fmt.Errorf("unsupported container create option: %s", name)
		}
	}
	if len(args) == 0 {
		return ContainerRequest{}, fmt.Errorf("container %s requires an image", request.Operation)
	}
	request.Image = args[0]
	request.Command = append([]string{}, args[1:]...)
	return request, nil
}

func parseContainerMount(value string) (ContainerMount, error) {
	parts := strings.Split(value, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return ContainerMount{}, errors.New("invalid container mount")
	}
	mount := ContainerMount{Source: parts[0], Target: parts[1]}
	if len(parts) == 3 {
		if parts[2] != "ro" {
			return ContainerMount{}, errors.New("container mounts only support the ro option")
		}
		mount.ReadOnly = true
	}
	return mount, nil
}

func containerOptionValue(args []string, index int, inline string, hasInline bool) (string, int, error) {
	if hasInline {
		if inline == "" {
			return "", index, errors.New("container option requires a value")
		}
		return inline, index, nil
	}
	if index+1 >= len(args) || args[index+1] == "" {
		return "", index, errors.New("container option requires a value")
	}
	return args[index+1], index + 1, nil
}
