/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const hostConfigurationRecheck = 5000

type followedConfiguration struct {
	source  string
	local   *os.File
	content []byte
}

func followsHost(conf, nameserver string) bool {
	switch conf {
	case "/etc/resolv.conf":
		return nameserver == ""
	case "/etc/hosts":
		return true
	}
	return false
}

func (f *followedConfiguration) update(content []byte) (bool, error) {
	if bytes.Equal(content, f.content) {
		return false, nil
	}
	if _, err := f.local.WriteAt(content, 0); err != nil {
		return false, err
	}
	if err := f.local.Truncate(int64(len(content))); err != nil {
		return false, err
	}
	f.content = append([]byte(nil), content...)
	return true, nil
}

func followedDirectories(sources []string) []string {
	seen := map[string]bool{}
	directories := []string{}
	add := func(directory string) {
		if !seen[directory] {
			seen[directory] = true
			directories = append(directories, directory)
		}
	}
	for _, source := range sources {
		add(filepath.Dir(source))
		if resolved, err := filepath.EvalSymlinks(source); err == nil {
			add(filepath.Dir(resolved))
		}
	}
	return directories
}

func (c *SpawnCmd) followHostConfiguration(worker *grantMountWorker) {
	watch, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		c.spawnVerbose("host configuration watch unavailable: ", err)
		return
	}
	defer unix.Close(watch)
	sources := make([]string, 0, len(c.followedConfiguration))
	for _, followed := range c.followedConfiguration {
		sources = append(sources, followed.source)
	}
	watched := map[string]bool{}
	const events = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB
	refresh := func() bool {
		contents := make([][]byte, len(c.followedConfiguration))
		err := worker.Do(func() error {
			for _, directory := range followedDirectories(sources) {
				if watched[directory] {
					continue
				}
				if _, addErr := unix.InotifyAddWatch(watch, directory, events); addErr == nil {
					watched[directory] = true
				}
			}
			for index, source := range sources {
				if content, readErr := os.ReadFile(source); readErr == nil {
					contents[index] = content
				}
			}
			return nil
		})
		if err != nil {
			return false
		}
		for index := range c.followedConfiguration {
			if contents[index] == nil {
				continue
			}
			followed := &c.followedConfiguration[index]
			if changed, updateErr := followed.update(contents[index]); updateErr != nil {
				c.spawnVerbose("could not follow ", followed.source, ": ", updateErr)
			} else if changed {
				c.spawnVerbose("Followed the host ", followed.source)
			}
		}
		return true
	}
	if !refresh() {
		return
	}
	buffer := make([]byte, 4096)
	descriptors := []unix.PollFd{{Fd: int32(watch), Events: unix.POLLIN}}
	for {
		ready, pollErr := unix.Poll(descriptors, hostConfigurationRecheck)
		if pollErr == unix.EINTR {
			continue
		}
		if pollErr != nil {
			return
		}
		if ready > 0 {
			read, readErr := unix.Read(watch, buffer)
			if readErr != nil && readErr != unix.EINTR && readErr != unix.EAGAIN {
				return
			}
			if readErr == nil && read <= 0 {
				return
			}
		}
		if !refresh() {
			return
		}
	}
}
