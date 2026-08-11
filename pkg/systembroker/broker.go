/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ProtocolVersion  = 1
	OperationNotify  = "notify"
	OperationOpenURI = "open-uri"
	maxRequestSize   = 16 << 10
)

type Request struct {
	Version   int      `json:"version"`
	Token     string   `json:"token"`
	Operation string   `json:"operation"`
	Args      []string `json:"args"`
}

type Response struct {
	Error string `json:"error,omitempty"`
}

type Options struct {
	SocketPath     string
	Token          string
	AllowNotify    bool
	AllowOpenURI   bool
	OpenURICommand string
	CommandTimeout time.Duration
	AuthorizePeer  func(*net.UnixConn) error
	Notify         func(context.Context, []string) error
}

func (o Options) validate() error {
	if o.SocketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if len(o.Token) < 32 {
		return errors.New("system broker token is too short")
	}
	if !o.AllowNotify && !o.AllowOpenURI {
		return errors.New("system broker has no enabled operations")
	}
	return nil
}

func (o Options) openURICommand() string {
	if o.OpenURICommand != "" {
		return o.OpenURICommand
	}
	return "xdg-open"
}

func (o Options) commandTimeout() time.Duration {
	if o.CommandTimeout > 0 {
		return o.CommandTimeout
	}
	return 10 * time.Second
}

func (o Options) authorize(connection *net.UnixConn) error {
	if o.AuthorizePeer != nil {
		return o.AuthorizePeer(connection)
	}
	return authorizePeer(connection)
}

func (o Options) notify(ctx context.Context, args []string) error {
	if o.Notify != nil {
		return o.Notify(ctx, args)
	}
	return sendNotification(ctx, args)
}

func Serve(ctx context.Context, options Options) error {
	if err := options.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.SocketPath), 0700); err != nil {
		return fmt.Errorf("create system broker directory: %w", err)
	}
	if err := removeSocket(options.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: options.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for system broker: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(options.SocketPath)
	}()
	if err := os.Chmod(options.SocketPath, 0600); err != nil {
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
			handle(connection, options)
		}()
	}
}

func Call(socketPath, token, operation string, args []string) error {
	if socketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if len(token) < 32 {
		return errors.New("system broker token is too short")
	}
	connection, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connect to system broker: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return fmt.Errorf("set system broker deadline: %w", err)
	}
	if err := json.NewEncoder(connection).Encode(Request{Version: ProtocolVersion, Token: token, Operation: operation, Args: args}); err != nil {
		return fmt.Errorf("write system broker request: %w", err)
	}
	response := Response{}
	if err := json.NewDecoder(io.LimitReader(connection, maxRequestSize)).Decode(&response); err != nil {
		return fmt.Errorf("read system broker response: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func handle(connection *net.UnixConn, options Options) {
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	response := Response{}
	if err := options.authorize(connection); err != nil {
		response.Error = "system broker denied the caller"
	} else {
		request := Request{}
		if err := json.NewDecoder(io.LimitReader(connection, maxRequestSize)).Decode(&request); err != nil {
			response.Error = "invalid system broker request"
		} else if err := authorizeRequest(request, options.Token); err != nil {
			response.Error = "system broker denied the request"
		} else if err := execute(request, options); err != nil {
			response.Error = err.Error()
		}
	}
	_ = json.NewEncoder(connection).Encode(response)
}

func authorizeRequest(request Request, token string) error {
	if request.Version != ProtocolVersion {
		return errors.New("unsupported system broker protocol")
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(token)) != 1 {
		return errors.New("invalid system broker token")
	}
	if request.Operation != OperationNotify && request.Operation != OperationOpenURI {
		return errors.New("unsupported system broker operation")
	}
	return nil
}

func execute(request Request, options Options) error {
	ctx, cancel := context.WithTimeout(context.Background(), options.commandTimeout())
	defer cancel()
	switch request.Operation {
	case OperationNotify:
		if !options.AllowNotify {
			return errors.New("desktop notifications are not permitted")
		}
		if err := validateNotificationArgs(request.Args); err != nil {
			return err
		}
		if err := options.notify(ctx, request.Args); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("system integration backend timed out: notification service")
			}
			return fmt.Errorf("system integration backend failed: notification service: %w", err)
		}
		return nil
	case OperationOpenURI:
		if !options.AllowOpenURI {
			return errors.New("opening URIs is not permitted")
		}
		if err := validateURIArgs(request.Args); err != nil {
			return err
		}
		path, err := exec.LookPath(options.openURICommand())
		if err != nil {
			return fmt.Errorf("system integration backend is unavailable: %s", options.openURICommand())
		}
		command := exec.Command(path, request.Args...)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return fmt.Errorf("system integration backend failed: %s", options.openURICommand())
		}
		if err := command.Process.Release(); err != nil {
			return fmt.Errorf("release system integration backend: %s", options.openURICommand())
		}
		return nil
	}
	return errors.New("unsupported system broker operation")
}

func validateNotificationArgs(args []string) error {
	if len(args) == 0 || len(args) > 24 {
		return errors.New("invalid notification request")
	}
	for _, arg := range args {
		if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
			return errors.New("invalid notification request")
		}
	}
	return nil
}

func validateURIArgs(args []string) error {
	if len(args) != 1 || len(args[0]) > 4096 || strings.ContainsRune(args[0], '\x00') {
		return errors.New("invalid URI request")
	}
	parsed, err := url.ParseRequestURI(args[0])
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid URI request")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
		return nil
	default:
		return errors.New("URI scheme is not permitted")
	}
}

func authorizePeer(connection *net.UnixConn) error {
	var credentials *unix.Ucred
	var controlErr error
	raw, err := connection.SyscallConn()
	if err != nil {
		return err
	}
	err = raw.Control(func(fd uintptr) {
		credentials, controlErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return err
	}
	if controlErr != nil {
		return controlErr
	}
	if credentials == nil || credentials.Uid != uint32(os.Getuid()) {
		return errors.New("unexpected system broker peer")
	}
	return nil
}

func removeSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect system broker socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("system broker path is not a socket")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("system broker socket has an unexpected owner")
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return errors.New("system broker socket is already active")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale system broker socket: %w", err)
	}
	return nil
}
