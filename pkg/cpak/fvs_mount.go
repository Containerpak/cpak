/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fvs-lab/fvs2d/fvs2dpb"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	fvsManagerTimeout     = 5 * time.Second
	fvsPreparationTimeout = 30 * time.Minute
)

type storageBackend string

const (
	storageBackendAuto   storageBackend = "auto"
	storageBackendNative storageBackend = "native"
	storageBackendFUSE   storageBackend = "fuse"
)

var (
	errStoragePreparationRequired = errors.New("application storage is not prepared")
	errStorageServiceMissing      = errors.New("cpak storage service is not installed")
)

func (c *Cpak) prepareLayerMount(statePath string, layers []string) (string, string, string, error) {
	backend, err := configuredStorageBackend()
	if err != nil {
		return "", "", "", err
	}
	if backend == storageBackendFUSE {
		return c.prepareFVSMount(statePath, layers)
	}
	lowerDirs, err := c.preparedLayerDirectories(layers)
	if err == nil {
		return "", strings.Join(lowerDirs, ":"), "", nil
	}
	if !errors.Is(err, errStoragePreparationRequired) {
		return "", "", "", err
	}
	if c.storageDriver == nil {
		if _, _, serviceErr := findStorageDriverService(); errors.Is(serviceErr, errStorageServiceMissing) {
			if _, legacyErr := c.legacyLayerDirectories(layers); legacyErr == nil {
				return "", "", "", nil
			}
			if _, legacyErr := findStorageService(); legacyErr == nil {
				return c.prepareFVSMount(statePath, layers)
			}
			return "", "", "", errStorageServiceMissing
		} else if serviceErr != nil {
			return "", "", "", serviceErr
		}
	}
	if err := c.ensureFVSLayers(layers); err != nil {
		return "", "", "", err
	}
	prepare := func() error {
		lowerDirs, err = c.prepareStorageDriver(layers)
		return err
	}
	if c.storagePreparation != nil {
		err = c.storagePreparation(prepare)
	} else {
		err = prepare()
	}
	if errors.Is(err, errStorageServiceMissing) {
		if _, legacyErr := findStorageService(); legacyErr == nil {
			return c.prepareFVSMount(statePath, layers)
		}
	}
	if err != nil {
		return "", "", "", err
	}
	return "", strings.Join(lowerDirs, ":"), "", nil
}

func (c *Cpak) legacyLayerDirectories(layers []string) ([]string, error) {
	lowerDirs := make([]string, 0, len(layers))
	for index := len(layers) - 1; index >= 0; index-- {
		root := c.GetInStoreDir("layers", layers[index])
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, errStoragePreparationRequired
		}
		lowerDirs = append(lowerDirs, root)
	}
	return lowerDirs, nil
}

func (c *Cpak) prepareFVSMount(statePath string, layers []string) (string, string, string, error) {
	if len(layers) == 0 {
		return "", "", "", errors.New("no layers specified")
	}
	if err := c.ensureFVSLayers(layers); err != nil {
		return "", "", "", err
	}
	mountPoint := filepath.Join(statePath, "lower")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return "", "", "", err
	}
	managerSocket, err := c.fvsManagerSocket()
	if err != nil {
		return "", "", "", err
	}

	unlock, err := c.lockFVSManager()
	if err != nil {
		return "", "", "", err
	}
	defer unlock()
	client, connection, err := c.ensureFVSManager(managerSocket)
	if err != nil {
		return "", "", "", err
	}
	defer connection.Close()
	spec := &fvs2dpb.MountSpec{
		MountPoint:          mountPoint,
		Layers:              make([]*fvs2dpb.Layer, 0, len(layers)),
		ClearPrivilegedBits: true,
	}
	for _, layer := range layers {
		spec.Layers = append(spec.Layers, &fvs2dpb.Layer{RepositoryPath: c.fvsLayerPath(layer)})
	}
	ctx, cancel := context.WithTimeout(c.Ctx, fvsPreparationTimeout)
	defer cancel()
	mount, err := client.CreateMount(ctx, &fvs2dpb.CreateMountRequest{Spec: spec})
	if err != nil {
		return "", "", "", fmt.Errorf("mount FVS layers: %w", err)
	}
	return mount.GetId(), mountPoint, managerSocket, nil
}

func configuredStorageBackend() (storageBackend, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CPAK_STORAGE_BACKEND"))) {
	case "", "auto":
		return storageBackendAuto, nil
	case "native", "overlay", "overlayfs":
		return storageBackendNative, nil
	case "fuse":
		return storageBackendFUSE, nil
	default:
		return "", fmt.Errorf("unsupported cpak storage backend %q", os.Getenv("CPAK_STORAGE_BACKEND"))
	}
}

func (c *Cpak) withFVSMount(layers []string, run func(string) error) error {
	states, err := c.GetInStoreDirMkdir("states")
	if err != nil {
		return err
	}
	state, err := os.MkdirTemp(states, ".fvs-view-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	mountID, mountPath, managerSocket, err := c.prepareFVSMount(state, layers)
	if err != nil {
		return err
	}
	defer c.releaseFVSMount(mountID, managerSocket)
	return run(mountPath)
}

// PrepareApplicationStorage builds reusable native layer checkouts.
func (c *Cpak) PrepareApplicationStorage(app types.Application) error {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	components, err := c.resolveLayerDependenciesFromStore(app, store)
	if err != nil {
		store.Close()
		return err
	}
	addons, err := c.resolveEnabledAddonsFromStore(app, store)
	if err != nil {
		store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	layers := composedLayers(app, components, addons)
	if len(layers) == 0 {
		return nil
	}
	prepare := func() error {
		_, err := c.prepareStorageDriver(layers)
		return err
	}
	if c.storagePreparation != nil {
		return c.storagePreparation(prepare)
	}
	return prepare()
}

func (c *Cpak) WithApplicationFilesystem(app types.Application, run func(string) error) error {
	backend, err := configuredStorageBackend()
	if err != nil {
		return err
	}
	if backend == storageBackendFUSE {
		return c.withFVSMount(app.ParsedLayers, run)
	}
	lowerDirs, err := c.preparedLayerDirectories(app.ParsedLayers)
	if errors.Is(err, errStoragePreparationRequired) {
		if c.storageDriver == nil {
			if _, _, serviceErr := findStorageDriverService(); errors.Is(serviceErr, errStorageServiceMissing) {
				if lowerDirs, err = c.legacyLayerDirectories(app.ParsedLayers); err != nil {
					if _, legacyErr := findStorageService(); legacyErr == nil {
						return c.withFVSMount(app.ParsedLayers, run)
					}
					return errStorageServiceMissing
				}
			} else if serviceErr != nil {
				return serviceErr
			} else {
				lowerDirs, err = c.prepareStorageDriver(app.ParsedLayers)
			}
		} else {
			lowerDirs, err = c.prepareStorageDriver(app.ParsedLayers)
		}
	}
	if err != nil {
		return err
	}
	return withNativeOverlayView(lowerDirs, run)
}

func withNativeOverlayView(lowerDirs []string, run func(string) error) error {
	if len(lowerDirs) == 0 {
		return errors.New("no layers specified")
	}
	mount, err := exec.LookPath("mount")
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "cpak-overlay-view-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	merged := filepath.Join(root, "merged")
	if err := os.Mkdir(merged, 0o700); err != nil {
		return err
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyReader.Close()
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		readyWriter.Close()
		return err
	}
	defer controlWriter.Close()

	options := "lowerdir=" + strings.Join(lowerDirs, ":") + ",userxattr"
	script := `"$1" -t overlay overlay -o "$2" "$3"; printf '\001' >&3; read -r _ || true; umount "$3"`
	command := nativeNamespaceCommand("/bin/sh", []string{"-eu", "-c", script, "sh", mount, options, merged}, namespaceOptions{})
	var stderr bytes.Buffer
	command.Stdin = controlReader
	command.Stderr = &stderr
	command.ExtraFiles = []*os.File{readyWriter}
	if err := command.Start(); err != nil {
		readyWriter.Close()
		controlReader.Close()
		return err
	}
	readyWriter.Close()
	controlReader.Close()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	ready := make(chan error, 1)
	go func() {
		buffer := []byte{0}
		_, readErr := io.ReadFull(readyReader, buffer)
		if readErr == nil && buffer[0] != 1 {
			readErr = errors.New("invalid overlay readiness response")
		}
		ready <- readErr
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = command.Process.Kill()
			commandErr := <-exited
			return fmt.Errorf("prepare overlay view: %w: %s", errors.Join(err, commandErr), strings.TrimSpace(stderr.String()))
		}
	case err := <-exited:
		return fmt.Errorf("prepare overlay view: %w: %s", err, strings.TrimSpace(stderr.String()))
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		commandErr := <-exited
		return errors.Join(errors.New("prepare overlay view: readiness timeout"), commandErr)
	}

	view := filepath.Join("/proc", strconv.Itoa(command.Process.Pid), "root", strings.TrimPrefix(merged, "/"))
	runErr := run(view)
	_ = controlWriter.Close()
	select {
	case err := <-exited:
		if err != nil {
			err = fmt.Errorf("release overlay view: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return errors.Join(runErr, err)
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		commandErr := <-exited
		return errors.Join(runErr, errors.New("release overlay view: timeout"), commandErr)
	}
}

func (c *Cpak) releaseFVSMount(id, managerSocket string) error {
	if id == "" {
		return nil
	}
	if managerSocket == "" {
		var err error
		managerSocket, err = c.legacyFVSManagerSocket()
		if err != nil {
			return err
		}
	}
	unlock, err := c.lockFVSManager()
	if err != nil {
		return err
	}
	defer unlock()
	client, connection, err := c.connectFVSManager(managerSocket)
	if err != nil {
		return nil
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(c.Ctx, fvsManagerTimeout)
	defer cancel()
	if _, err := client.Unmount(ctx, &fvs2dpb.UnmountRequest{MountId: id, Mode: fvs2dpb.UnmountMode_UNMOUNT_MODE_LAZY}); err != nil {
		return fmt.Errorf("unmount FVS layers: %w", err)
	}
	return nil
}

func (c *Cpak) cleanupFVSMount(id, mountPath, managerSocket string) error {
	err := c.releaseFVSMount(id, managerSocket)
	if mountPath == "" {
		return err
	}
	var status syscall.Statfs_t
	if statErr := syscall.Statfs(mountPath, &status); !errors.Is(statErr, syscall.ENOTCONN) {
		return err
	}
	for _, name := range []string{"fusermount3", "fusermount"} {
		binary, lookupErr := exec.LookPath(name)
		if lookupErr != nil {
			continue
		}
		if unmountErr := exec.Command(binary, "-uz", mountPath).Run(); unmountErr != nil {
			return errors.Join(err, unmountErr)
		}
		return err
	}
	return errors.Join(err, errors.New("fusermount is not installed"))
}

func (c *Cpak) fvsMountAlive(id, mountPath, managerSocket string) bool {
	if mountPath == "" {
		return false
	}
	if id == "" {
		return nativeLayerCheckoutsAlive(mountPath)
	}
	if managerSocket == "" {
		var err error
		managerSocket, err = c.legacyFVSManagerSocket()
		if err != nil {
			return false
		}
	}
	client, connection, err := c.connectFVSManager(managerSocket)
	if err != nil {
		return false
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(c.Ctx, 250*time.Millisecond)
	defer cancel()
	mount, err := client.GetMount(ctx, &fvs2dpb.GetMountRequest{MountId: id})
	if err != nil {
		return false
	}
	if mount.GetSpec().GetMountPoint() != mountPath {
		return false
	}
	var status syscall.Statfs_t
	return syscall.Statfs(mountPath, &status) == nil && uint64(status.Type) == uint64(unix.FUSE_SUPER_MAGIC)
}

func nativeLayerCheckoutsAlive(lowerDirs string) bool {
	for _, root := range strings.Split(lowerDirs, ":") {
		if root == "" {
			return false
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return false
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "checkout.json")); err != nil {
			return false
		}
	}
	return true
}

func (c *Cpak) containerLayerMountAlive(container types.Container) bool {
	if container.FVSLayerMountId == "" && container.FVSLayerMountPath == "" {
		return true
	}
	return c.fvsMountAlive(container.FVSLayerMountId, container.FVSLayerMountPath, container.FVSManagerSocketPath)
}

func (c *Cpak) ensureFVSManager(socket string) (fvs2dpb.Fvs2DClient, *grpc.ClientConn, error) {
	client, connection, err := c.connectFVSManager(socket)
	if err == nil {
		return client, connection, nil
	}
	_ = os.Remove(socket)
	binary, err := findStorageService()
	if err != nil {
		return nil, nil, err
	}
	logPath := filepath.Join(filepath.Dir(socket), "storaged.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	command := exec.Command(binary, "--control", "unix:"+socket, "--root", c.Options.StorePath)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return nil, nil, err
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	deadline := time.Now().Add(fvsManagerTimeout)
	for time.Now().Before(deadline) {
		client, connection, err = c.connectFVSManager(socket)
		if err == nil {
			return client, connection, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, nil, fmt.Errorf("start cpak storage service: %w", err)
}

func (c *Cpak) connectFVSManager(socket string) (fvs2dpb.Fvs2DClient, *grpc.ClientConn, error) {
	connection, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	client := fvs2dpb.NewFvs2DClient(connection)
	ctx, cancel := context.WithTimeout(c.Ctx, 250*time.Millisecond)
	defer cancel()
	if _, err := client.Probe(ctx, &emptypb.Empty{}); err != nil {
		connection.Close()
		return nil, nil, err
	}
	return client, connection, nil
}

func (c *Cpak) fvsManagerSocket() (string, error) {
	namespace, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return "", fmt.Errorf("read mount namespace: %w", err)
	}
	return c.fvsManagerSocketForNamespace(namespace)
}

func (c *Cpak) legacyFVSManagerSocket() (string, error) {
	directory, err := c.GetInStoreDirMkdir("runtime", "storage")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(c.Options.StorePath))
	return filepath.Join(directory, hex.EncodeToString(digest[:8])+".sock"), nil
}

func (c *Cpak) fvsManagerSocketForNamespace(namespace string) (string, error) {
	directory, err := c.GetInStoreDirMkdir("runtime", "storage")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(c.Options.StorePath + "\x00" + namespace))
	return filepath.Join(directory, hex.EncodeToString(digest[:8])+".sock"), nil
}

func (c *Cpak) lockFVSManager() (func(), error) {
	directory, err := c.GetInStoreDirMkdir("locks")
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(filepath.Join(directory, "storaged.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func findStorageService() (string, error) {
	configured := os.Getenv("CPAK_STORAGE_BINARY")
	if configured == "" {
		configured = os.Getenv("CPAK_FVS2D_BINARY")
	}
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return configured, nil
		}
		return "", errStorageServiceMissing
	}
	if cpakBinary, err := getCpakBinary(); err == nil {
		for _, name := range []string{"cpak-fvs2d", "fvs2d"} {
			candidate := filepath.Join(filepath.Dir(cpakBinary), name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	for _, name := range []string{"cpak-fvs2d", "fvs2d"} {
		if binary, err := exec.LookPath(name); err == nil {
			return binary, nil
		}
	}
	return "", errStorageServiceMissing
}
