/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fvs-lab/fvs2d/fvs2dpb"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
)

const fvsManagerTimeout = 5 * time.Second

var errStorageServiceMissing = errors.New("cpak storage service is not installed")

func (c *Cpak) prepareLayerMount(statePath string, layers []string) (string, string, error) {
	if _, err := findStorageService(); err == nil {
		return c.prepareFVSMount(statePath, layers)
	} else if !errors.Is(err, errStorageServiceMissing) {
		return "", "", err
	}
	for _, layer := range layers {
		info, err := os.Stat(c.GetInStoreDir("layers", layer))
		if err != nil || !info.IsDir() {
			return "", "", errStorageServiceMissing
		}
	}
	return "", "", nil
}

func (c *Cpak) prepareFVSMount(statePath string, layers []string) (string, string, error) {
	if len(layers) == 0 {
		return "", "", errors.New("no layers specified")
	}
	if err := c.ensureFVSLayers(layers); err != nil {
		return "", "", err
	}
	mountPoint := filepath.Join(statePath, "lower")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return "", "", err
	}

	unlock, err := c.lockFVSManager()
	if err != nil {
		return "", "", err
	}
	defer unlock()
	client, connection, err := c.ensureFVSManager()
	if err != nil {
		return "", "", err
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
	ctx, cancel := context.WithTimeout(c.Ctx, fvsManagerTimeout)
	defer cancel()
	mount, err := client.CreateMount(ctx, &fvs2dpb.CreateMountRequest{Spec: spec})
	if err != nil {
		return "", "", fmt.Errorf("mount FVS layers: %w", err)
	}
	return mount.GetId(), mountPoint, nil
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
	mountID, mountPath, err := c.prepareFVSMount(state, layers)
	if err != nil {
		return err
	}
	defer c.releaseFVSMount(mountID)
	return run(mountPath)
}

func (c *Cpak) WithApplicationFilesystem(app types.Application, run func(string) error) error {
	return c.withFVSMount(app.ParsedLayers, run)
}

func (c *Cpak) releaseFVSMount(id string) error {
	if id == "" {
		return nil
	}
	unlock, err := c.lockFVSManager()
	if err != nil {
		return err
	}
	defer unlock()
	client, connection, err := c.connectFVSManager()
	if err != nil {
		return nil
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(c.Ctx, fvsManagerTimeout)
	defer cancel()
	if _, err := client.Unmount(ctx, &fvs2dpb.UnmountRequest{MountId: id, Mode: fvs2dpb.UnmountMode_UNMOUNT_MODE_LAZY}); err != nil {
		return fmt.Errorf("unmount FVS layers: %w", err)
	}
	mounts, err := client.ListMounts(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	if len(mounts.GetMounts()) == 0 {
		_, _ = client.Shutdown(ctx, &fvs2dpb.ShutdownRequest{Mode: fvs2dpb.UnmountMode_UNMOUNT_MODE_LAZY})
	}
	return nil
}

func (c *Cpak) cleanupFVSMount(id, mountPath string) error {
	err := c.releaseFVSMount(id)
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

func (c *Cpak) fvsMountAlive(id, mountPath string) bool {
	if id == "" || mountPath == "" {
		return false
	}
	client, connection, err := c.connectFVSManager()
	if err != nil {
		return false
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(c.Ctx, 250*time.Millisecond)
	defer cancel()
	mount, err := client.GetMount(ctx, &fvs2dpb.GetMountRequest{MountId: id})
	if err != nil || mount.GetSpec().GetMountPoint() != mountPath {
		return false
	}
	var status syscall.Statfs_t
	return syscall.Statfs(mountPath, &status) == nil
}

func (c *Cpak) containerLayerMountAlive(container types.Container) bool {
	if container.FVSLayerMountId == "" && container.FVSLayerMountPath == "" {
		return true
	}
	return c.fvsMountAlive(container.FVSLayerMountId, container.FVSLayerMountPath)
}

func (c *Cpak) ensureFVSManager() (fvs2dpb.Fvs2DClient, *grpc.ClientConn, error) {
	client, connection, err := c.connectFVSManager()
	if err == nil {
		return client, connection, nil
	}
	socket, err := c.fvsManagerSocket()
	if err != nil {
		return nil, nil, err
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
		client, connection, err = c.connectFVSManager()
		if err == nil {
			return client, connection, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, nil, fmt.Errorf("start cpak storage service: %w", err)
}

func (c *Cpak) connectFVSManager() (fvs2dpb.Fvs2DClient, *grpc.ClientConn, error) {
	socket, err := c.fvsManagerSocket()
	if err != nil {
		return nil, nil, err
	}
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
	directory, err := c.GetInStoreDirMkdir("runtime", "storage")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(c.Options.StorePath))
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
		for _, name := range []string{"cpak-storaged", "cpak-fvs2d", "fvs2d"} {
			candidate := filepath.Join(filepath.Dir(cpakBinary), name)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
				return candidate, nil
			}
		}
	}
	for _, name := range []string{"cpak-storaged", "cpak-fvs2d", "fvs2d"} {
		if binary, err := exec.LookPath(name); err == nil {
			return binary, nil
		}
	}
	return "", errStorageServiceMissing
}
