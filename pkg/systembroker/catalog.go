/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	userManagerDestination = "org.freedesktop.systemd1"
	userManagerPath        = dbus.ObjectPath("/org/freedesktop/systemd1")
	userManagerInterface   = "org.freedesktop.systemd1.Manager"
)

var loadUserManagerEnvironment = userManagerEnvironment

type Policy struct {
	AllowNotify  bool `json:"allow_notify,omitempty"`
	AllowOpenURI bool `json:"allow_open_uri,omitempty"`
	// Kept so old policy files remain readable. The broker does not trust a
	// container policy to select the host desktop used for URI handlers.
	DesktopEnvironment    []string              `json:"desktop_environment,omitempty"`
	AllowHostApplications bool                  `json:"allow_host_applications,omitempty"`
	Applications          map[string]string     `json:"applications,omitempty"`
	RuntimeDirectory      string                `json:"runtime_directory,omitempty"`
	ContainerOwner        string                `json:"container_owner,omitempty"`
	ContainerCapabilities map[string]bool       `json:"container_capabilities,omitempty"`
	ContainerPaths        []ContainerPathGrant  `json:"container_paths,omitempty"`
	CpakCapabilities      map[string]bool       `json:"cpak_capabilities,omitempty"`
	FilePicker            FilePickerPolicy      `json:"file_picker,omitempty"`
	FilePickerPaths       []FilePickerPathGrant `json:"file_picker_paths,omitempty"`
	FilePickerApplication string                `json:"file_picker_application,omitempty"`
	FilePickerOrigin      string                `json:"file_picker_origin,omitempty"`
	FileGrantSocketPath   string                `json:"file_grant_socket_path,omitempty"`
	FileGrantStorePath    string                `json:"file_grant_store_path,omitempty"`
}

func PolicyPath(directory, token string) (string, error) {
	if len(token) < 32 {
		return "", errors.New("system broker token is too short")
	}
	digest := sha256.Sum256([]byte(token))
	return filepath.Join(directory, hex.EncodeToString(digest[:])+".json"), nil
}

func WritePolicy(directory, token string, policy Policy) error {
	path, err := PolicyPath(directory, token)
	if err != nil {
		return err
	}
	if err = preparePolicyDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".policy-")
	if err != nil {
		return fmt.Errorf("create system broker policy: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		err = json.NewEncoder(temporary).Encode(policy)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write system broker policy: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish system broker policy: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open system broker policy directory: %w", err)
	}
	err = directoryFile.Sync()
	if closeErr := directoryFile.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("sync system broker policy directory: %w", err)
	}
	return nil
}

func preparePolicyDirectory(directory string) error {
	if directory == "" || !filepath.IsAbs(directory) {
		return errors.New("system broker policy directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create system broker policy directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect system broker policy directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("system broker policy directory is not private")
	}
	if info.Mode().Perm() != 0700 {
		if err = os.Chmod(directory, 0700); err != nil {
			return fmt.Errorf("restrict system broker policy directory: %w", err)
		}
	}
	return nil
}

func RemovePolicy(directory, token string) error {
	path, err := PolicyPath(directory, token)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove system broker policy: %w", err)
	}
	return nil
}

func ServeCatalog(ctx context.Context, socketPath, directory string) error {
	fallbackDesktopEnvironment := CaptureDesktopEnvironment(os.Environ(), "")
	return serve(ctx, socketPath, authorizePeer, func(request Request) (Options, error) {
		return resolveCatalogPolicy(socketPath, directory, currentDesktopEnvironment(fallbackDesktopEnvironment), request)
	})
}

func currentDesktopEnvironment(fallback []string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	environment, err := loadUserManagerEnvironment(ctx)
	if err != nil {
		return fallback
	}
	desktopEnvironment := CaptureDesktopEnvironment(environment, "")
	if len(desktopEnvironment) == 0 {
		return fallback
	}
	return desktopEnvironment
}

func userManagerEnvironment(ctx context.Context) ([]string, error) {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	object := connection.Object(userManagerDestination, userManagerPath)
	var value dbus.Variant
	if err = object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, userManagerInterface, "Environment").Store(&value); err != nil {
		return nil, err
	}
	environment, ok := value.Value().([]string)
	if !ok {
		return nil, errors.New("user manager desktop environment is invalid")
	}
	return environment, nil
}

func resolveCatalogPolicy(socketPath, directory string, desktopEnvironment []string, request Request) (Options, error) {
	policy, err := readPolicy(directory, request.Token)
	if err != nil {
		return Options{}, errors.New("system broker denied the request")
	}
	return Options{
		SocketPath:            socketPath,
		Token:                 request.Token,
		AllowNotify:           policy.AllowNotify,
		AllowOpenURI:          policy.AllowOpenURI,
		DesktopEnvironment:    desktopEnvironment,
		AllowHostApplications: policy.AllowHostApplications,
		Applications:          policy.Applications,
		RuntimeDirectory:      policy.RuntimeDirectory,
		ContainerOwner:        policy.ContainerOwner,
		ContainerCapabilities: policy.ContainerCapabilities,
		ContainerPaths:        policy.ContainerPaths,
		CpakCapabilities:      policy.CpakCapabilities,
		FilePicker:            policy.FilePicker,
		FilePickerPaths:       policy.FilePickerPaths,
		FilePickerApplication: policy.FilePickerApplication,
		FilePickerOrigin:      policy.FilePickerOrigin,
		FileGrantSocketPath:   policy.FileGrantSocketPath,
		FileGrantStorePath:    policy.FileGrantStorePath,
	}, nil
}

func readPolicy(directory, token string) (Policy, error) {
	path, err := PolicyPath(directory, token)
	if err != nil {
		return Policy{}, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Policy{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		unix.Close(fd)
		return Policy{}, errors.New("open system broker policy")
	}
	defer file.Close()
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&0077 != 0 || stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return Policy{}, errors.New("invalid system broker policy")
	}
	var policy Policy
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func serve(ctx context.Context, socketPath string, authorize func(*net.UnixConn) error, resolve func(Request) (Options, error)) error {
	if socketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0700); err != nil {
		return fmt.Errorf("create system broker directory: %w", err)
	}
	if err := removeSocket(socketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for system broker: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0600); err != nil {
		return fmt.Errorf("restrict system broker socket: %w", err)
	}

	var connections sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	defer close(done)
	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				connections.Wait()
				return nil
			}
			return fmt.Errorf("accept system broker request: %w", acceptErr)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer connection.Close()
			handleResolved(connection, authorize, resolve)
		}()
	}
}
