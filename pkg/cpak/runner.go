/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// cpakSocketPath is where the cpak service listens for the nested run requests
// of the containers. It is bind mounted into every container by cmd/spawn.
const cpakSocketPath = "/tmp/cpak.sock"

const (
	// socketDialTimeout bounds a single connection attempt to the service.
	socketDialTimeout = 2 * time.Second
	// socketWaitTimeout bounds the wait for a service that is starting up.
	socketWaitTimeout  = 10 * time.Second
	socketWaitInterval = 25 * time.Millisecond
	// nestedDrainTimeout bounds the wait for the pty of a nested run to flush
	// once the run is over.
	nestedDrainTimeout = 2 * time.Second
)

// ServiceCommand is the cpak command that serves the socket, the one
// prepareSocketListener re-executes.
const ServiceCommand = "service"

var errSocketInUse = errors.New("the socket already has a listener")

var isVerbose bool

// Run runs the given binary from the given application. The binary can be
// specified as a path or as a name. If the binary is specified as a name,
// the first binary matching the given name will be executed. To execute a
// unexported binary, the binary name must be prefixed with a "@".
//
// Note: binaries specified with the "@" prefix are not guaranteed to be
// available in required applications, so it is recommended to use them only
// for debugging purposes and handle the error case when the binary is not
// available, e.g. in shell scripts.
func (c *Cpak) Run(origin string, version string, branch string, commit string, release string, binary string, verbose bool, extraArgs ...string) (err error) {
	return c.RunInstance(origin, version, branch, commit, release, "", binary, verbose, extraArgs...)
}

func (c *Cpak) RunInstance(origin string, version string, branch string, commit string, release string, instance string, binary string, verbose bool, extraArgs ...string) (err error) {
	isVerbose = verbose
	parentAppCpakId, isNested := getNested()
	if isNested {
		logger.Println("Running in nested mode...")
		return c.RunNested(parentAppCpakId, origin, version, branch, commit, release, binary, extraArgs...)
	}

	err = c.prepareSocketListener()
	if err != nil {
		return
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil || app.CpakId == "" {
		_ = store.Close()
		return fmt.Errorf("no application found for origin %s and version/criteria %s: %w", origin, version, err)
	}
	if err := store.Close(); err != nil {
		return err
	}

	return c.runApplicationInstance(app, resolvedOverride(app), instance, binary, verbose, false, extraArgs...)
}

func (c *Cpak) RunAuthorized(params types.RequestParams, verbose bool) error {
	isVerbose = verbose
	authorized, err := c.authorizeNestedRun(params)
	if err != nil {
		return err
	}
	return c.runApplication(authorized.child, authorized.override, authorized.binary, verbose, true, params.ExtraArgs...)
}

func (c *Cpak) runApplication(app types.Application, override types.Override, binary string, verbose, nested bool, extraArgs ...string) error {
	return c.runApplicationInstance(app, override, "", binary, verbose, nested, extraArgs...)
}

func (c *Cpak) runApplicationInstance(app types.Application, override types.Override, instance, binary string, verbose, nested bool, extraArgs ...string) error {
	startTime := time.Now()
	var container types.Container
	var err error
	if nested {
		container, err = c.PrepareNestedContainer(app, override)
	} else {
		container, err = c.PrepareContainerInstance(app, override, instance)
	}
	if err != nil {
		return err
	}
	if nested {
		defer c.cleanupNestedContainer(container)
	}
	if verbose {
		logger.Printf("Container creation took %s", time.Since(startTime))
	}

	if nested {
		command := append([]string{binary}, extraArgs...)
		return c.ExecInContainer(app, container, command)
	}

	command := []string{}
	actualBinaryName := binary
	if strings.HasPrefix(binary, "@") {
		actualBinaryName = binary[1:]
	} else if strings.HasPrefix(binary, "/") {
		actualBinaryName = binary[strings.LastIndex(binary, "/")+1:]
	}

	foundBinary := false
	if strings.HasPrefix(binary, "@") { // Unexported binary, assume it exists
		command = append(command, actualBinaryName)
		command = append(command, extraArgs...)
		foundBinary = true
	} else {
		for _, b := range app.ParsedBinaries {
			if filepath.Base(b) == actualBinaryName {
				command = append(command, b) // Use the full path from manifest if available
				command = append(command, extraArgs...)
				foundBinary = true
				break
			}
		}
	}

	if !foundBinary {
		if len(app.ParsedBinaries) == 0 {
			return fmt.Errorf("no exported binaries found for application %s", app.Name)
		}
		// Fallback or error if specific binary not found among exported ones
		// For now, let's assume if not unexported and not found, it's an error or use default.
		logger.Printf("Warning: binary '%s' not explicitly found in manifest, attempting to run '%s'", actualBinaryName, app.ParsedBinaries[0])
		command = append(command, app.ParsedBinaries[0])
		command = append(command, extraArgs...)
	}

	return c.ExecInContainer(app, container, command)
}

// prepareSocketListener makes sure the cpak service is listening before a
// container is started, so that a nested run has somewhere to connect to.
func (c *Cpak) prepareSocketListener() (err error) {
	if socketIsLive(cpakSocketPath) {
		return
	}

	// Run cpak service without attaching to the current process
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	cmd := exec.Command(cpakBinary, ServiceCommand)
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("cannot start the cpak service: %w", err)
	}
	err = cmd.Process.Release()
	if err != nil {
		return fmt.Errorf("cannot detach the cpak service: %w", err)
	}
	return waitForSocket(cpakSocketPath, socketWaitTimeout)
}

// socketIsLive reports whether something is accepting connections on path.
func socketIsLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, socketDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitForSocket waits for a listener to answer on path, giving up after
// timeout instead of spinning forever on a service that never started.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if socketIsLive(path) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no cpak service answered on %s within %s", path, timeout)
		}
		time.Sleep(socketWaitInterval)
	}
}

// clearStaleSocket removes a leftover socket file, leaving alone one that still
// has a listener behind it.
func clearStaleSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if socketIsLive(path) {
		return errSocketInUse
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("cannot remove the stale socket %s: %w", path, err)
	}
	return nil
}

func (c *Cpak) StartSocketListener() (err error) {
	return c.serveSocket(cpakSocketPath)
}

// serveSocket serves nested run requests on socketPath. Starting a service
// while another one is already listening is not an error, it is a no-op.
func (c *Cpak) serveSocket(socketPath string) (err error) {
	logger.Println("Preparing socket listener...")
	err = clearStaleSocket(socketPath)
	if errors.Is(err, errSocketInUse) {
		logger.Printf("A cpak service is already listening on %s.", socketPath)
		return nil
	}
	if err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()

	// the socket is a door into the host, only its owner may knock
	err = os.Chmod(socketPath, 0600)
	if err != nil {
		return err
	}
	logger.Printf("Waiting for connections on %s...", listener.Addr())

	for {
		var conn net.Conn
		conn, err = listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			logger.Printf("Error accepting connection: %v", err)
			continue
		}

		go c.handleSocketConnection(conn)
	}
}

func (c *Cpak) handleSocketConnection(conn net.Conn) {
	defer conn.Close()
	writer := newFrameWriter(conn)

	// the first frame a container sends is always the JSON encoded
	// RequestParams struct, which the host uses to decide whether the
	// requested nested cpak can be run at all
	kind, payload, err := readFrame(conn)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			logger.Printf("Error reading request: %v", err)
		}
		return
	}
	if kind != frameRequest {
		sendErrorFrame(writer, fmt.Errorf("expected a request frame, got type %d", kind))
		return
	}

	var params types.RequestParams
	err = json.Unmarshal(payload, &params)
	if err != nil {
		logger.Printf("Error parsing JSON request: %v", err)
		sendErrorFrame(writer, fmt.Errorf("invalid JSON request"))
		return
	}
	err = validateNestedRequest(params)
	if err != nil {
		logger.Printf("Rejected request from the container: %v", err)
		sendErrorFrame(writer, err)
		return
	}

	logger.Printf("Received request from the container: %+v", params)
	err = c.serveNestedRun(conn, writer, params)
	if err != nil {
		logger.Printf("Error running the nested cpak: %v", err)
		sendErrorFrame(writer, err)
	}
}

// serveNestedRun re-executes cpak on the host for a container that asked for a
// nested run, bridging its pty over the framed connection and answering with
// the exit status of the run.
func (c *Cpak) serveNestedRun(conn net.Conn, writer *frameWriter, params types.RequestParams) (err error) {
	logger.Printf("Running another cpak container in nested mode...")

	args, err := BuildNestedRunArgs(params)
	if err != nil {
		return err
	}
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return err
	}

	// we need to create a PTY to run the nested cpak and allow the
	// bidirectional communication between the host and the container
	ptyMaster, ptySlave, err := pty.Open()
	if err != nil {
		return fmt.Errorf("error creating PTY: %w", err)
	}
	defer ptyMaster.Close()

	cmd := exec.Command(cpakBinary, args...)
	cmd.Stdin = ptySlave
	cmd.Stdout = ptySlave
	cmd.Stderr = ptySlave

	// set the process group so that the termination signal is
	// forwarded to the shell process
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err = cmd.Start(); err != nil {
		ptySlave.Close()
		return fmt.Errorf("error starting the nested cpak: %w", err)
	}

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		pumpOutput(writer, ptyMaster)
	}()

	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		readClientFrames(conn, ptyMaster, cmd)
	}()

	cmdExited := make(chan error, 1)
	go func() {
		cmdExited <- cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-cmdExited:
	case <-clientGone:
		logger.Println("Client connection closed or errored. Terminating nested cpak process.")
		signalProcessGroup(cmd, syscall.SIGKILL)
		waitErr = <-cmdExited
	}
	ptySlave.Close()

	// the exit status comes last, so nothing the application printed is
	// left behind it
	select {
	case <-outputDone:
	case <-time.After(nestedDrainTimeout):
	}

	code, err := exitCodeFromError(waitErr)
	if err != nil {
		return err
	}
	logger.Printf("Nested cpak command exited with status %d.", code)
	if err = writer.write(frameExit, encodeExitStatus(code)); err != nil {
		// the container hung up first, there is nobody left to tell
		logger.Printf("Cannot send the exit status: %v", err)
	}
	return nil
}

// pumpOutput forwards what the nested run prints to the container.
func pumpOutput(writer *frameWriter, ptyMaster *os.File) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := ptyMaster.Read(buffer)
		if n > 0 {
			if writeErr := writer.write(frameOutput, buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// readClientFrames feeds the stdin and the signals of the container into the
// nested run, and returns as soon as the container is gone.
func readClientFrames(conn net.Conn, ptyMaster *os.File, cmd *exec.Cmd) {
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Printf("Error reading from the container: %v", err)
			}
			return
		}

		switch kind {
		case frameStdin:
			if _, err = ptyMaster.Write(payload); err != nil {
				return
			}
		case frameStdinEOF:
			// a pty has no write side to close, the run simply stops
			// receiving input from here on
		case frameSignal:
			sig, sigErr := decodeSignal(payload)
			if sigErr != nil {
				logger.Printf("Ignoring signal from the container: %v", sigErr)
				continue
			}
			signalProcessGroup(cmd, sig)
		default:
			logger.Printf("Ignoring unexpected frame type %d from the container", kind)
		}
	}
}

// signalProcessGroup delivers sig to the whole nested process group.
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// exitCodeFromError turns the result of exec.Cmd.Wait into an exit status,
// following the shell convention for a process killed by a signal.
func exitCodeFromError(err error) (int, error) {
	if err == nil {
		return 0, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, err
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code, nil
	}
	return 1, nil
}

// BuildNestedRunArgs builds the host side argv for a nested run. The request
// travels encoded in a single argument because the arguments of the application
// are not cpak's to parse: -i or --version would otherwise be read as cpak
// flags long before the requested binary is reached.
func BuildNestedRunArgs(params types.RequestParams) ([]string, error) {
	encoded, err := EncodeNestedRequest(params)
	if err != nil {
		return nil, err
	}
	return []string{"run", "--nested-request", encoded}, nil
}

func sendErrorFrame(writer *frameWriter, errToSend error) {
	if err := writer.write(frameError, []byte(errToSend.Error())); err != nil {
		logger.Printf("Error sending the failure frame: %v", err)
	}
}

func (c *Cpak) RunNested(parentAppCpakId string, origin string, version string, branch string, commit string, release string, binary string, extraArgs ...string) (err error) {
	logger.Println("Running another cpak container in nested mode...")

	// the RequestParams struct is used by the server to check if the cpak
	// which is running, has the ability to run the specified nested cpak
	params := types.RequestParams{
		Action:      "run",
		ParentAppId: parentAppCpakId,
		Origin:      origin,
		Version:     version,
		Branch:      branch,
		Commit:      commit,
		Release:     release,
		Binary:      binary,
		ExtraArgs:   extraArgs,
	}
	err = validateNestedRequest(params)
	if err != nil {
		return err
	}
	requestData, err := json.Marshal(params)
	if err != nil {
		logger.Printf("Error encoding request data as JSON: %v", err)
		return
	}

	// start a connection to the socket
	conn, err := net.DialTimeout("unix", cpakSocketPath, socketDialTimeout)
	if err != nil {
		return fmt.Errorf("cannot reach the cpak service on %s: %w", cpakSocketPath, err)
	}
	defer conn.Close()

	writer := newFrameWriter(conn)
	logger.Printf("Sending request to the socket: %s", requestData)
	err = writer.write(frameRequest, requestData)
	if err != nil {
		logger.Printf("Error sending request: %v", err)
		return
	}

	go pumpStdin(writer, os.Stdin)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- readNestedResponse(conn, os.Stdout)
	}()

	forwarded := 0
	for {
		select {
		case err = <-resultCh:
			return err
		case sig := <-sigCh:
			number, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			forwarded++
			if forwarded > 1 {
				// the host is not answering, stop waiting for it
				logger.Println("Interrupt received, closing nested connection.")
				return &types.ExitError{Code: 128 + int(number)}
			}
			if err = writer.write(frameSignal, encodeSignal(number)); err != nil {
				return err
			}
		}
	}
}

// pumpStdin forwards the stdin of the container to the nested run.
func pumpStdin(writer *frameWriter, in io.Reader) {
	buffer := make([]byte, 32*1024)
	for {
		n, err := in.Read(buffer)
		if n > 0 {
			if writeErr := writer.write(frameStdin, buffer[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			_ = writer.write(frameStdinEOF, nil)
			return
		}
	}
}

// readNestedResponse mirrors what the nested run prints and returns its exit
// status as an error the CLI can exit with.
func readNestedResponse(conn net.Conn, out io.Writer) error {
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the cpak service closed the connection without an exit status")
			}
			return err
		}

		switch kind {
		case frameOutput:
			if _, err = out.Write(payload); err != nil {
				return err
			}
		case frameExit:
			code, err := decodeExitStatus(payload)
			if err != nil {
				return err
			}
			if code != 0 {
				return &types.ExitError{Code: code}
			}
			return nil
		case frameError:
			return fmt.Errorf("the cpak service refused the nested run: %s", payload)
		default:
			return fmt.Errorf("unexpected frame type %d from the cpak service", kind)
		}
	}
}
