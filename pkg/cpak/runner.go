package cpak

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// Run runs the given binary from the given application. The binary can be
// specified as a path or as a name. If the binary is specified as a name,
// the first binary matching the given name will be executed. To execute a
// unexported binary, the binary name must be prefixed with a "@".
//
// Note: binaries specified with the "@" prefix are not guaranteed to be
// available in required applications, so it is recommended to use them only
// for debugging purposes and handle the error case when the binary is not
// available, e.g. in shell scripts.
func (c *Cpak) Run(origin string, version string, branch string, commit string, release string, binary string, extraArgs ...string) (err error) {
	parentAppId, isNested := getNested()
	if isNested {
		fmt.Println("Running in nested mode...")
		return c.RunNested(parentAppId, origin, version, branch, commit, release, binary, extraArgs...)
	}

	err = c.prepareSocketListener()
	if err != nil {
		return
	}

	workDir := os.Getenv("PWD")
	if !strings.HasPrefix(workDir, "/home") {
		workDir = "/"
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return
	}

	app, err := store.GetApplicationByOrigin(origin, version, branch, commit, release)
	if err != nil || app.Id == "" {
		return fmt.Errorf("no application found for origin %s and version %s: %s", origin, version, err)
	}

	// Get the override for the given application, we try to load the user
	// override first, if it does not exist, we use the application's one
	var override types.Override
	override, err = LoadOverride(app.Origin, app.Version)
	if err != nil {
		override = app.Override
	}

	var container types.Container
	container, err = c.PrepareContainer(app, override)
	if err != nil {
		return
	}

	command := []string{}
	if strings.HasPrefix(binary, "@") {
		command = append(command, binary[1:])
		command = append(command, extraArgs...)
		return c.ExecInContainer(override, container, command)
	} else if strings.HasPrefix(binary, "/") {
		binary = binary[strings.LastIndex(binary, "/")+1:]
	}

	for _, _binary := range app.Binaries {
		_binary = _binary[strings.LastIndex(_binary, "/")+1:]
		if _binary == binary {
			break
		}
	}

	if app.Id == "" {
		if version == "" {
			return fmt.Errorf("no application found for origin %s", origin)
		}
		return fmt.Errorf("no application found for origin %s and version %s", origin, version)
	}

	if len(app.Binaries) == 0 {
		return fmt.Errorf("no exported binaries found for application %s", app.Name)
	}

	command = append(command, app.Binaries[0])
	command = append(command, extraArgs...)
	err = c.ExecInContainer(override, container, command)
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

	// the socket listens on TCP port 12345
	// TODO: here we use a TCP connection because it is easier to debug but
	// 		 we should use the Unix socket which is defined in
	// 		 /run/user/<uid>/cpak.sock and already mounted in the container
	// uid := os.Getuid()
	// socketPath := fmt.Sprintf("/run/user/%d/cpak.sock", uid)
	// err = os.Chmod(socketPath, 0777)
	// if err != nil {
	// 	return err
	// }
	// listener, err := net.Listen("unix", socketPath)
	listener, err := net.Listen("tcp", "localhost:12345")
	if err != nil {
		return err
	}
	defer listener.Close()
	//fmt.Printf("Waiting for connections on %s...\n", socketPath)
	fmt.Printf("Waiting for connections on localhost:12345...\n")

	for {
		var conn net.Conn
		conn, err = listener.Accept()
		if err != nil {
			fmt.Printf("Error accepting connection: %v\n", err)
			continue
		}

		defer conn.Close()

		buffer := make([]byte, 1024)
		var n int
		n, err = conn.Read(buffer)
		if err != nil {
			fmt.Printf("Error reading request: %v\n", err)
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
				conn.Close()
				return
			}

			// set up the channels to communicate with the host
			go func() {
				io.Copy(conn, ptyMaster)
				ptyMaster.Close()
				conn.Close()
			}()
			go func() {
				io.Copy(ptyMaster, conn)
				ptyMaster.Close()
				conn.Close()
			}()

			// here the effective command is executed in the host to run the
			// requested nested cpak container
			args := []string{
				"run",
				params.Origin,
				"--version", params.Version,
				"--branch", params.Branch,
				"--commit", params.Commit,
				"--release", params.Release,
				"--",
				params.Binary,
			}
			args = append(args, params.ExtraArgs...)
			cmd := exec.Command("cpak", args...)
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
				conn.Close()
				return
			}

			// we need to create a channel to handle termination signals
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

			// setting up a goroutine to wait for the shell process to exit
			go func() {
				cmd.Wait()
				ptySlave.Close()
			}()

			// a goroutine to handle termination signals
			go func() {
				<-sigCh
				fmt.Println("Closing the connection and shell process...")
				sendSuccessResponse(conn)
				conn.Close()
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}()
		default:
			fmt.Printf("Unknown request: %s\n", params.Action)
			sendErrorResponse(conn, fmt.Errorf("unknown request: %s", params.Action))
			return
		}
	}
}

func sendSuccessResponse(conn net.Conn) {
	response := "OK"
	_, err := conn.Write([]byte(response))
	if err != nil {
		fmt.Printf("Error sending success response: %v\n", err)
	}

	fmt.Printf("Sent response to the container: %s\n", response)
}

func sendErrorResponse(conn net.Conn, err error) {
	response := fmt.Sprintf("Error: %v", err)
	_, writeErr := conn.Write([]byte(response))
	if writeErr != nil {
		fmt.Printf("Error sending error response: %v\n", writeErr)
	}

	fmt.Printf("Sent response to the container: %s\n", response)
}

// RunNested runs the given binary from the given application in nested mode.
func (c *Cpak) RunNested(parentAppId string, origin string, version string, branch string, commit string, release string, binary string, extraArgs ...string) (err error) {
	fmt.Println("Running another cpak container in nested mode...")

	// uid := os.Getuid()
	// socketPath := fmt.Sprintf("/run/user/%d/cpak.sock", uid)

	// the RequestParams struct is used by the server to check if the cpak
	// which is running, has the ability to run the specified nested cpak
	params := types.RequestParams{
		Action:      "run",
		ParentAppId: parentAppId,
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

	// here starts the connection to the socket
	// TODO: here we use a TCP connection because it is easier to debug but
	// 		 we should use the Unix socket which is defined in
	// 		 /run/user/<uid>/cpak.sock and already mounted in the container
	// conn, err := net.Dial("unix", socketPath)
	conn, err := net.Dial("tcp", "localhost:12345")
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

	go func() {
		_, err := io.Copy(conn, os.Stdin)
		if err != nil {
			fmt.Println("Error copying data to the server:", err)
		}
		close(doneCh)
	}()
	go func() {
		_, err := io.Copy(os.Stdout, conn)
		if err != nil {
			fmt.Println("Error copying data from the server:", err)
		}
		close(doneCh)
	}()

	// wait for the channels to be closed
	<-doneCh
	return
}
