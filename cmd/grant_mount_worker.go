/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"golang.org/x/sys/unix"
)

type grantMountWorker struct {
	requests chan grantMountRequest
	done     chan struct{}
}

type grantMountRequest struct {
	grant   filegrant.Grant
	sources grantproto.Sources
	result  chan grantMountResult
}

type grantMountResult struct {
	tree *os.File
	err  error
}

func startGrantMountWorker() (*grantMountWorker, error) {
	worker := &grantMountWorker{requests: make(chan grantMountRequest), done: make(chan struct{})}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer close(worker.done)
		if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
			ready <- fmt.Errorf("preserve host mount namespace: %w", err)
			return
		}
		if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
			ready <- fmt.Errorf("isolate host mount namespace: %w", err)
			return
		}
		ready <- nil
		for request := range worker.requests {
			tree, err := cloneGrantMount(request.grant, request.sources)
			request.result <- grantMountResult{tree: tree, err: err}
		}
	}()
	if err := <-ready; err != nil {
		close(worker.requests)
		<-worker.done
		return nil, err
	}
	return worker, nil
}

func (w *grantMountWorker) Clone(grant filegrant.Grant, sources grantproto.Sources) (*os.File, error) {
	if w == nil {
		return nil, errors.New("file grant mount worker is unavailable")
	}
	result := make(chan grantMountResult, 1)
	w.requests <- grantMountRequest{grant: grant, sources: sources, result: result}
	cloned := <-result
	return cloned.tree, cloned.err
}

func (w *grantMountWorker) Close() {
	if w == nil {
		return
	}
	close(w.requests)
	<-w.done
}

func cloneGrantMount(grant filegrant.Grant, sources grantproto.Sources) (*os.File, error) {
	path := grant.Source
	expected := sources.Selected
	if grant.Kind == filegrant.KindFile {
		path = filepath.Dir(grant.Source)
		expected = sources.Mount
	}
	if expected == nil {
		return nil, errors.New("file grant mount descriptor is required")
	}
	treeFD, err := unix.OpenTree(unix.AT_FDCWD, path, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("clone file grant mount: %w", err)
	}
	tree := os.NewFile(uintptr(treeFD), path)
	if tree == nil {
		_ = unix.Close(treeFD)
		return nil, errors.New("open cloned file grant mount")
	}
	treeInfo, err := tree.Stat()
	if err != nil {
		_ = tree.Close()
		return nil, fmt.Errorf("inspect cloned file grant mount: %w", err)
	}
	expectedInfo, err := expected.Stat()
	if err != nil {
		_ = tree.Close()
		return nil, fmt.Errorf("inspect file grant mount descriptor: %w", err)
	}
	if !os.SameFile(treeInfo, expectedInfo) {
		_ = tree.Close()
		return nil, errors.New("file grant source changed while cloning")
	}
	attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC)
	if grant.Access == filegrant.AccessReadOnly {
		attributes |= unix.MOUNT_ATTR_RDONLY
	}
	if err = unix.MountSetattr(treeFD, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: attributes}); err != nil {
		_ = tree.Close()
		return nil, fmt.Errorf("restrict file grant mount: %w", err)
	}
	return tree, nil
}
