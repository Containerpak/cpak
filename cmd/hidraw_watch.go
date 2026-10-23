/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mirkobrombin/cpak/pkg/tools"
	"golang.org/x/sys/unix"
)

func isHidrawNode(name string) bool {
	rest, ok := strings.CutPrefix(name, "hidraw")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *SpawnCmd) watchHidraw(worker *grantMountWorker) {
	watch, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		c.spawnVerbose("hidraw watch unavailable: ", err)
		return
	}
	defer unix.Close(watch)
	present := []string{}
	err = worker.Do(func() error {
		if _, addErr := unix.InotifyAddWatch(watch, "/dev", unix.IN_CREATE|unix.IN_DELETE); addErr != nil {
			return addErr
		}
		entries, readErr := os.ReadDir("/dev")
		if readErr != nil {
			return readErr
		}
		for _, entry := range entries {
			if isHidrawNode(entry.Name()) {
				present = append(present, entry.Name())
			}
		}
		return nil
	})
	if err != nil {
		c.spawnVerbose("hidraw watch unavailable: ", err)
		return
	}
	for _, name := range present {
		c.attachHidraw(worker, name)
	}
	buffer := make([]byte, 4096)
	for {
		read, readErr := unix.Read(watch, buffer)
		if readErr == unix.EINTR {
			continue
		}
		if readErr != nil || read <= 0 {
			return
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= read; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buffer[offset]))
			end := offset + unix.SizeofInotifyEvent + int(event.Len)
			if end > read {
				break
			}
			name := strings.TrimRight(string(buffer[offset+unix.SizeofInotifyEvent:end]), "\x00")
			offset = end
			if !isHidrawNode(name) {
				continue
			}
			switch {
			case event.Mask&unix.IN_CREATE != 0:
				c.attachHidraw(worker, name)
			case event.Mask&unix.IN_DELETE != 0:
				detachHidraw(name)
			}
		}
	}
}

func (c *SpawnCmd) attachHidraw(worker *grantMountWorker, name string) {
	var tree *os.File
	err := worker.Do(func() error {
		descriptor, openErr := unix.OpenTree(unix.AT_FDCWD, filepath.Join("/dev", name), unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
		if openErr != nil {
			return openErr
		}
		cloned := os.NewFile(uintptr(descriptor), name)
		info, statErr := cloned.Stat()
		if statErr != nil {
			_ = cloned.Close()
			return statErr
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			_ = cloned.Close()
			return errors.New("not a character device")
		}
		attributes := &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NOEXEC}
		if setErr := unix.MountSetattr(descriptor, "", unix.AT_EMPTY_PATH, attributes); setErr != nil {
			_ = cloned.Close()
			return setErr
		}
		tree = cloned
		return nil
	})
	if err != nil {
		c.spawnVerbose("hidraw ", name, " not shared: ", err)
		return
	}
	defer tree.Close()
	target := filepath.Join("/dev", name)
	detachHidraw(name)
	placeholder, err := os.OpenFile(target, os.O_CREATE|os.O_RDONLY, 0600)
	if err != nil {
		c.spawnVerbose("hidraw ", name, " not shared: ", err)
		return
	}
	_ = placeholder.Close()
	if err = tools.AttachDescriptorMountPrepared(int(tree.Fd()), target); err != nil {
		_ = os.Remove(target)
		c.spawnVerbose("hidraw ", name, " not shared: ", err)
	}
}

func detachHidraw(name string) {
	target := filepath.Join("/dev", name)
	for syscall.Unmount(target, syscall.MNT_DETACH) == nil {
	}
	_ = os.Remove(target)
}
