/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cpak

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/mirkobrombin/cpak/pkg/types"
)

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
	isVerbose = verbose
	var startTime time.Time
	if verbose {
		startTime = time.Now()
	}

	parentAppCpakId, isNested := getNested()
	if isNested {
		fmt.Println("Running in nested mode...")
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
	defer store.Close()

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil || app.CpakId == "" {
		return fmt.Errorf("no application found for origin %s and version/criteria %s: %w", origin, version, err)
	}

	// Get the override for the given application, we try to load the user
	// override first, if it does not exist, we use the application's one
	var appOverride types.Override
	userOverride, errLoad := LoadOverride(app.Origin, app.Version)
	if errLoad == nil && !reflect.DeepEqual(userOverride, types.NewOverride()) { // Consider user override if loaded and not default
		appOverride = userOverride
	} else {
		appOverride = app.ParsedOverride
	}

	container, err := c.PrepareContainer(app, appOverride)
	if err != nil {
		return
	}

	if verbose {
		elapsed := time.Since(startTime)
		fmt.Printf("Container creation took %s\n", elapsed)
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
		fmt.Printf("Warning: binary '%s' not explicitly found in manifest, attempting to run '%s'\n", actualBinaryName, app.ParsedBinaries[0])
		command = append(command, app.ParsedBinaries[0])
		command = append(command, extraArgs...)
	}

	err = c.ExecInContainer(app, container, command)
	return
}

// prepareSocketListener prepares the socket listener to be used by containers to spawn nested containers.
func (c *Cpak) prepareSocketListener() (err error) {
	// Run cpak start-service without attaching to the current process
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	cmd := exec.Command(cpakBinary, "start-service")
	err = cmd.Start()
	if err != nil {
		return
	}
	err = cmd.Process.Release()
	if err != nil {
		return
	}
	return
}

func (c *Cpak) StartSocketListener() (err error) {
	fmt.Println("Preparing socket listener...")
	_ = os.Remove("/tmp/cpak.sock")

	// the socket listens on /tmp/cpak.sock
	listener, err := net.Listen("unix", "/tmp/cpak.sock")
	if err != nil {
		return err
	}
	defer listener.Close()
	fmt.Printf("Waiting for connections on %s...\n", listener.Addr())

	for {
		var conn net.Conn
		conn, err = listener.Accept()
		if err != nil {
			fmt.Printf("Error accepting connection: %v\n", err)
			continue
		}

		go c.handleSocketConnection(conn)
	}
}

func (c *Cpak) handleSocketConnection(conn net.Conn) {
	defer conn.Close()

	buffer := make([]byte, 2048)
	var n int
	var err error
	n, err = conn.Read(buffer)
	if err != nil {
		if err != io.EOF {
			fmt.Printf("Error reading request: %v\n", err)
		}
		return
	}

	// the cpak container sends different requests to the socket, those
	// can be both JSON encoded or plain text but the first one must always
	// be a JSON encoded RequestParams struct which is used by the server
	// to check if the cpak which is running, has the ability to run the
	// specified nested cpak
	var params types.RequestParams
	err = json.Unmarshal(buffer[:n], &params)
	if err != nil {
		fmt.Printf("Error parsing JSON request: %v\n", err)
		sendErrorResponse(conn, fmt.Errorf("invalid JSON request"))
		return
	}

	fmt.Printf("Received request from the container: %+v\n", params)

	switch params.Action {
	case "run":
		fmt.Printf("Running another cpak container in nested mode...\n")

		// we need to create a PTY to run the nested cpak and allow the
		// bidirectional communication between the host and the container
		var ptyMaster, ptySlave *os.File
		ptyMaster, ptySlave, err = pty.Open()
		if err != nil {
			fmt.Println("Error creating PTY:", err)
			sendErrorResponse(conn, fmt.Errorf("error creating PTY"))
			return
		}
		defer ptyMaster.Close() // Ensure master is closed

		done := make(chan struct{})
		go func() {
			defer close(done)
			io.Copy(ptyMaster, conn)
		}()
		go func() {
			io.Copy(conn, ptyMaster)
		}()

		args := []string{
			"run",
			params.Origin,
		}
		if params.Version != "" {
			args = append(args, "--version", params.Version)
		}
		if params.Branch != "" {
			args = append(args, "--branch", params.Branch)
		}
		if params.Commit != "" {
			args = append(args, "--commit", params.Commit)
		}
		if params.Release != "" {
			args = append(args, "--release", params.Release)
		}
		args = append(args, "--", params.Binary)
		args = append(args, params.ExtraArgs...)

		cpakBinary, _ := getCpakBinary()
		cmd := exec.Command(cpakBinary, args...)
		cmd.Stdin = ptySlave
		cmd.Stdout = ptySlave
		cmd.Stderr = ptySlave

		// set the process group so that the termination signal is
		// forwarded to the shell process
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}

		// start the shell process
		if err = cmd.Start(); err != nil {
			fmt.Println("Error starting shell:", err)
			sendErrorResponse(conn, fmt.Errorf("error starting shell"))
			ptySlave.Close()
			return
		}

		cmdExited := make(chan error, 1)
		go func() {
			cmdExited <- cmd.Wait()
			ptySlave.Close() // Close slave after command exits
		}()

		select {
		case err := <-cmdExited:
			if err != nil {
				fmt.Printf("Nested cpak command exited with error: %v\n", err)
			} else {
				fmt.Println("Nested cpak command exited successfully.")
			}
			sendSuccessResponse(conn)
		case <-done:
			fmt.Println("Client connection closed or errored. Terminating nested cpak process.")
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
	default:
		fmt.Printf("Unknown request: %s\n", params.Action)
		sendErrorResponse(conn, fmt.Errorf("unknown request: %s", params.Action))
	}
}

func sendSuccessResponse(conn net.Conn) {
	response := "OK"
	_, err := conn.Write([]byte(response))
	if err != nil {
	}
}

func sendErrorResponse(conn net.Conn, errToSend error) {
	response := fmt.Sprintf("Error: %v", errToSend)
	_, err := conn.Write([]byte(response))
	if err != nil {
	}
}

func (c *Cpak) RunNested(parentAppCpakId string, origin string, version string, branch string, commit string, release string, binary string, extraArgs ...string) (err error) {
	fmt.Println("Running another cpak container in nested mode...")

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
	requestData, err := json.Marshal(params)
	if err != nil {
		fmt.Printf("Error encoding request data as JSON: %v\n", err)
		return
	}

	// start a connection to the socket
	conn, err := net.Dial("unix", "/tmp/cpak.sock")
	if err != nil {
		return err
	}
	defer conn.Close()

	// the requestData is sent to the socket to validate the action
	// before setting up the channels to communicate with the host
	fmt.Printf("Sending request to the socket: %s\n", requestData)
	_, err = conn.Write(requestData)
	if err != nil {
		fmt.Printf("Error sending request: %v\n", err)
		return
	}

	// set up the channels to communicate with the host
	doneCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		io.Copy(os.Stdout, conn)
		close(doneCh)
	}()
	go func() {
		io.Copy(conn, os.Stdin)
		conn.(*net.UnixConn).CloseWrite()
	}()

	select {
	case <-doneCh:
	case <-sigCh:
		fmt.Println("Interrupt received, closing nested connection.")
	}
	return
}
