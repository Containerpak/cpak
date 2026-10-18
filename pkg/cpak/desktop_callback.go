/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const desktopCallbackDirectoryName = "desktop-callbacks"

func desktopCallbackDirectory() (string, error) {
	runtimeDirectory, err := sharedSystemBrokerRuntimeDirectory()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(runtimeDirectory, desktopCallbackDirectoryName)
	if err = securePrivateDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

// ResolveDesktopCallbackInstance returns the package instance that opened an
// OAuth request represented by one of the desktop launch arguments.
func (c *Cpak) ResolveDesktopCallbackInstance(origin string, arguments []string) (string, bool, error) {
	c.desktopCallbackOrigin = ""
	c.desktopCallbackInstance = ""
	directory := ""
	for _, argument := range arguments {
		if !systembroker.IsDesktopCallback(argument) {
			continue
		}
		if directory == "" {
			var err error
			directory, err = desktopCallbackDirectory()
			if err != nil {
				return "", false, err
			}
		}
		instance, found, resolveErr := systembroker.ResolveDesktopCallback(directory, argument, origin)
		if resolveErr != nil {
			return "", false, resolveErr
		}
		if found {
			c.desktopCallbackOrigin = origin
			c.desktopCallbackInstance = instance
			return instance, true, nil
		}
	}
	return "", false, nil
}

func (c *Cpak) desktopCallbackWaylandEndpoint(origin, instance string, container types.Container, override types.Override) waylandEndpoint {
	if !c.desktopLaunch || c.desktopCallbackOrigin != origin || c.desktopCallbackOrigin == "" ||
		c.desktopCallbackInstance != instance || container.Instance != instance || container.WaylandSocketPath == "" {
		return currentWaylandEndpoint(override)
	}
	return statWaylandEndpoint(containerWaylandEndpoint(container, override))
}
