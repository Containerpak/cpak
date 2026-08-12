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
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ProtocolVersion            = 1
	OperationNotify            = "notify"
	OperationOpenURI           = "open-uri"
	OperationLaunchApplication = "launch-application"
	maxRequestSize             = 16 << 10
)

type Request struct {
	Version     int               `json:"version"`
	Token       string            `json:"token"`
	Operation   string            `json:"operation"`
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`
}

type Options struct {
	SocketPath            string
	Token                 string
	AllowNotify           bool
	AllowOpenURI          bool
	AllowHostApplications bool
	OpenURICommand        string
	Applications          map[string]string
	RuntimeDirectory      string
	CommandTimeout        time.Duration
	AuthorizePeer         func(*net.UnixConn) error
	Notify                func(context.Context, []string) error
	LaunchApplication     func(context.Context, string, []string, []string) error
}

func (o Options) validate() error {
	if o.SocketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if len(o.Token) < 32 {
		return errors.New("system broker token is too short")
	}
	if !o.AllowNotify && !o.AllowOpenURI && !o.AllowHostApplications {
		return errors.New("system broker has no enabled operations")
	}
	if o.AllowHostApplications {
		if err := validateRuntimeDirectory(o.RuntimeDirectory); err != nil {
			return err
		}
		if o.Applications == nil {
			return errors.New("system broker application catalog is required")
		}
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
	return CallWithEnvironment(socketPath, token, operation, args, nil)
}

func CallWithEnvironment(socketPath, token, operation string, args []string, environment map[string]string) error {
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
	if err := json.NewEncoder(connection).Encode(Request{Version: ProtocolVersion, Token: token, Operation: operation, Args: args, Environment: environment}); err != nil {
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
	if request.Operation != OperationNotify && request.Operation != OperationOpenURI && request.Operation != OperationLaunchApplication {
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
	case OperationLaunchApplication:
		if !options.AllowHostApplications {
			return errors.New("host applications are not permitted")
		}
		desktopEntry, args, err := validateApplicationRequest(request.Args, options.Applications)
		if err != nil {
			return err
		}
		environment, err := applicationEnvironment(request.Environment, options.RuntimeDirectory)
		if err != nil {
			return err
		}
		if options.LaunchApplication != nil {
			return options.LaunchApplication(ctx, desktopEntry, args, environment)
		}
		path, err := exec.LookPath("gio")
		if err != nil {
			return errors.New("host application backend is unavailable: gio")
		}
		command := exec.Command(path, append([]string{"launch", desktopEntry}, args...)...)
		command.Env = mergeEnvironment(os.Environ(), environment)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return errors.New("host application backend failed: gio")
		}
		if err := command.Process.Release(); err != nil {
			return errors.New("release host application backend: gio")
		}
		return nil
	}
	return errors.New("unsupported system broker operation")
}

var displayPattern = regexp.MustCompile(`^:[0-9]+(?:\.[0-9]+)?$`)
var waylandDisplayPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateApplicationRequest(args []string, applications map[string]string) (string, []string, error) {
	if len(args) == 0 || len(args) > 33 || len(args[0]) != 64 {
		return "", nil, errors.New("invalid host application request")
	}
	for _, character := range args[0] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", nil, errors.New("invalid host application request")
		}
	}
	desktopEntry := applications[args[0]]
	if desktopEntry == "" || !filepath.IsAbs(desktopEntry) || filepath.Ext(desktopEntry) != ".desktop" {
		return "", nil, errors.New("host application is not in the catalog")
	}
	for _, arg := range args[1:] {
		if err := validateApplicationURI(arg); err != nil {
			return "", nil, errors.New("invalid host application request")
		}
	}
	return desktopEntry, args[1:], nil
}

func validateApplicationURI(value string) error {
	if len(value) > 4096 || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "-") {
		return errors.New("invalid application URI")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid application URI")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file", "http", "https", "mailto":
		return nil
	default:
		return errors.New("application URI scheme is not permitted")
	}
}

func applicationEnvironment(request map[string]string, runtimeDirectory string) ([]string, error) {
	environment := []string{}
	if display := request["WAYLAND_DISPLAY"]; display != "" {
		if !waylandDisplayPattern.MatchString(display) {
			return nil, errors.New("invalid Wayland display")
		}
		socket := filepath.Join(runtimeDirectory, display)
		info, err := os.Stat(socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("Wayland display is unavailable")
		}
		environment = append(environment, "WAYLAND_DISPLAY="+socket)
	}
	if display := request["DISPLAY"]; display != "" {
		if !displayPattern.MatchString(display) {
			return nil, errors.New("invalid X11 display")
		}
		environment = append(environment, "DISPLAY="+display)
	}
	if token := request["XDG_ACTIVATION_TOKEN"]; token != "" {
		if len(token) > 4096 || strings.ContainsRune(token, '\x00') {
			return nil, errors.New("invalid activation token")
		}
		environment = append(environment, "XDG_ACTIVATION_TOKEN="+token)
	}
	if len(environment) == 0 {
		return nil, errors.New("host application display is unavailable")
	}
	return environment, nil
}

func LoadApplicationCatalog(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read application catalog: %w", err)
	}
	if info.Size() > 4<<20 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("application catalog is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read application catalog: %w", err)
	}
	catalog := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode application catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("application catalog contains multiple JSON values")
	}
	for token, desktopEntry := range catalog {
		if _, _, err := validateApplicationRequest([]string{token}, map[string]string{token: desktopEntry}); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func validateRuntimeDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("system broker desktop runtime must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("system broker desktop runtime must be a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("system broker desktop runtime has an unexpected owner")
	}
	return nil
}

func mergeEnvironment(base, overrides []string) []string {
	names := make(map[string]bool, len(overrides))
	for _, value := range overrides {
		if name, _, found := strings.Cut(value, "="); found {
			names[name] = true
		}
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, value := range base {
		name, _, found := strings.Cut(value, "=")
		if found && names[name] {
			continue
		}
		result = append(result, value)
	}
	return append(result, overrides...)
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
