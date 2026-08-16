/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func (c *Cpak) prepareDesktopLaunchArguments(origin string, permissions []types.FilesystemPermission, container types.Container, arguments []string) ([]string, error) {
	paths, err := systemBrokerFilePickerPaths(permissions)
	if err != nil {
		return nil, err
	}
	rewritten := make([]string, 0, len(arguments))
	targets := make(map[string]string)
	grantArguments := false
	for _, argument := range arguments {
		switch argument {
		case desktopFileArgumentStart:
			if grantArguments {
				return nil, fmt.Errorf("desktop launch file arguments are nested")
			}
			grantArguments = true
			continue
		case desktopFileArgumentEnd:
			if !grantArguments {
				return nil, fmt.Errorf("desktop launch file arguments are not open")
			}
			grantArguments = false
			continue
		}
		if !grantArguments {
			rewritten = append(rewritten, argument)
			continue
		}
		selected, uri, ok := desktopLaunchFile(argument)
		if !ok {
			rewritten = append(rewritten, argument)
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(selected)
		if os.IsNotExist(resolveErr) {
			rewritten = append(rewritten, argument)
			continue
		}
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve desktop launch file: %w", resolveErr)
		}
		target, found := desktopLaunchPathTarget(resolved, paths)
		if !found {
			target, found = targets[resolved]
		}
		if !found {
			target, err = mountDesktopLaunchFile(origin, resolved, container.GrantSocketPath)
			if err != nil {
				return nil, err
			}
			targets[resolved] = target
		}
		if uri == nil {
			rewritten = append(rewritten, target)
			continue
		}
		copy := *uri
		copy.Host = ""
		copy.Path = target
		copy.RawPath = ""
		rewritten = append(rewritten, copy.String())
	}
	if grantArguments {
		return nil, fmt.Errorf("desktop launch file arguments are not closed")
	}
	return rewritten, nil
}

func desktopLaunchFile(argument string) (string, *url.URL, bool) {
	if filepath.IsAbs(argument) && !strings.ContainsRune(argument, '\x00') {
		return filepath.Clean(argument), nil, true
	}
	parsed, err := url.Parse(argument)
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") || parsed.Opaque != "" || parsed.User != nil || parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") || !filepath.IsAbs(parsed.Path) || strings.ContainsRune(parsed.Path, '\x00') {
		return "", nil, false
	}
	return filepath.Clean(parsed.Path), parsed, true
}

func desktopLaunchPathTarget(selected string, paths []systembroker.FilePickerPathGrant) (string, bool) {
	for _, path := range paths {
		relative, err := filepath.Rel(path.Source, selected)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if relative == "." {
			return path.Target, true
		}
		return filepath.Join(path.Target, relative), true
	}
	return "", false
}

func mountDesktopLaunchFile(origin, selected, socket string) (string, error) {
	grant, err := filegrant.Resolve(origin, selected, filegrant.AccessReadOnly, filegrant.LifetimeSession, false)
	if err != nil {
		return "", err
	}
	source, err := filegrant.OpenSource(grant)
	if err != nil {
		return "", err
	}
	defer source.Close()
	mountSource, err := filegrant.OpenMountSource(grant)
	if err != nil {
		return "", err
	}
	if mountSource != nil {
		defer mountSource.Close()
	}
	target, err := grantproto.Send(socket, grant, source, mountSource)
	if err != nil {
		return "", fmt.Errorf("grant desktop launch file: %w", err)
	}
	return target, nil
}
