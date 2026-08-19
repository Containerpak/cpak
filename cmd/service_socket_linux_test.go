/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// The whole nested path in one namespace: the service listens where the host
// resolver puts it, the spawn command binds that socket at the address the
// container is told to use, and a run started inside the container reaches the
// service through it and comes back with the status of the run.
//
// The spawn command used to take that mount source from CPAK_SERVICE_SOCKET,
// which names the container end of this very mount and which the process
// building the container hands to the spawn process along with everything else
// the container is to see. The service was therefore never mounted, and the
// name the container dialled was one any account on the machine could have
// created first.
func TestNestedRunReachesTheServiceThroughTheMountedSocket(t *testing.T) {
	if os.Getenv("CPAK_SERVICE_SOCKET_TEST") == "1" {
		runNestedServiceSocketTest(t)
		return
	}
	base := t.TempDir()
	command := exec.Command("unshare", "--user", "--map-root-user", "--mount", os.Args[0], "-test.run=^TestNestedRunReachesTheServiceThroughTheMountedSocket$")
	command.Env = append(os.Environ(), "CPAK_SERVICE_SOCKET_TEST=1", "CPAK_SERVICE_SOCKET_BASE="+base)
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("/proc/self/uid_map: Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("nested run subprocess: %v\n%s", err, output)
	}
}

func runNestedServiceSocketTest(t *testing.T) {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatalf("make the mount tree private: %v", err)
	}
	// Everything this test owns lives under /run, so that the container view of
	// /tmp can take the place of the host one without hiding it. It is also
	// what keeps the socket path inside the length a unix address allows.
	if err := syscall.Mount(os.Getenv("CPAK_SERVICE_SOCKET_BASE"), "/run", "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		t.Fatalf("isolate the runtime directory: %v", err)
	}
	defer syscall.Unmount("/run", syscall.MNT_DETACH)
	for _, directory := range []string{"/run/user", "/run/rootfs"} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}
	// A private /tmp, and in it the name the container is told to dial, held by
	// somebody who is not the service. This is the machine the finding
	// describes: whoever creates /tmp/cpak.sock first holds it, and a mount
	// source read from the container address would hand this listener every
	// nested run the container makes.
	if err := syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, ""); err != nil {
		t.Fatalf("isolate /tmp: %v", err)
	}
	defer syscall.Unmount("/tmp", syscall.MNT_DETACH)
	decoy, err := net.Listen("unix", serviceSocketTarget)
	if err != nil {
		t.Fatalf("plant the decoy socket: %v", err)
	}
	defer decoy.Close()
	reached := make(chan struct{}, 1)
	go func() {
		for {
			connection, acceptErr := decoy.Accept()
			if acceptErr != nil {
				return
			}
			reached <- struct{}{}
			_ = connection.Close()
		}
	}()

	t.Setenv("XDG_RUNTIME_DIR", "/run/user")
	t.Setenv("CPAK_SERVICE_SOCKET", "")

	// The service re-executes cpak for every nested run it accepts, so it is
	// pointed at a stand in that records what it was asked to do.
	recorded := "/run/argv"
	writeFakeCpak(t, "/run/cpak", recorded, 7)
	original := os.Args[0]
	os.Args[0] = "/run/cpak"
	t.Cleanup(func() { os.Args[0] = original })

	hostSocket, err := cpak.HostServiceSocketPath()
	if err != nil {
		t.Fatalf("resolve the service socket: %v", err)
	}
	if strings.HasPrefix(hostSocket, "/tmp/") {
		t.Fatalf("the service listens in a shared directory: %s", hostSocket)
	}
	service := &cpak.Cpak{}
	served := make(chan error, 1)
	go func() { served <- service.StartSocketListener() }()
	waitForServiceSocket(t, hostSocket, served)

	// From here the process runs with the environment the spawn command is
	// really handed: the container is told the service answers at
	// /tmp/cpak.sock, and that variable travels to the spawn process together
	// with everything else the container is to see.
	t.Setenv("CPAK_SERVICE_SOCKET", serviceSocketTarget)

	// The spawn side binds the socket the host resolved onto the address the
	// container was told to use.
	spawn := &SpawnCmd{ServiceSocket: hostSocket}
	grant, mounted, err := spawn.mountServiceSocket("/run/rootfs")
	if err != nil {
		t.Fatalf("mount the service socket: %v", err)
	}
	if !mounted || grant.Path != serviceSocketTarget {
		t.Fatalf("the service socket was not bound at %s: mounted=%v grant=%+v", serviceSocketTarget, mounted, grant)
	}

	// From here on the process sees what the container sees: /tmp is the one
	// the rootfs carries, and the only cpak.sock in it is the bound socket.
	if err = syscall.Mount("/run/rootfs/tmp", "/tmp", "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		t.Fatalf("enter the container view of /tmp: %v", err)
	}
	if _, err = os.Stat("/tmp/cpak.sock"); err != nil {
		t.Fatalf("the container cannot see the service socket: %v", err)
	}

	err = service.RunNested(strings.Repeat("ab", 32), "github.com/example/app", "", "main", "", "", "app", "--version")
	var exitErr *types.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the nested run did not reach the service: %v", err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("status: got %d, want 7", exitErr.Code)
	}

	argv, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatalf("read the recorded argv: %v", err)
	}
	arguments := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n")
	if len(arguments) != 3 || arguments[0] != "run" || arguments[1] != "--nested-request" {
		t.Fatalf("the service ran %v", arguments)
	}
	request, err := cpak.DecodeNestedRequest(arguments[2])
	if err != nil {
		t.Fatalf("the service received an undecodable request: %v", err)
	}
	if request.Origin != "github.com/example/app" || request.Binary != "app" {
		t.Fatalf("the request lost its subject: %+v", request)
	}
	select {
	case <-reached:
		t.Fatal("the nested run was handed to the listener that took the name first")
	default:
	}
}

// The spawn process is handed CPAK_SERVICE_SOCKET along with the rest of the
// container environment, so a socket named only by that variable must not be
// bound: it is the container end of the mount, and on the host the same name
// belongs to whoever created it first.
func TestSpawnTakesTheServiceSocketFromItsFlagAlone(t *testing.T) {
	directory, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	planted := filepath.Join(directory, "cpak.sock")
	listener, err := net.Listen("unix", planted)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	t.Setenv("CPAK_SERVICE_SOCKET", planted)

	grant, mounted, err := (&SpawnCmd{}).mountServiceSocket(t.TempDir())
	if err != nil {
		t.Fatalf("mount the service socket: %v", err)
	}
	if mounted {
		t.Fatalf("a socket named only by the environment was bound: %+v", grant)
	}
}

// A name the host resolved still has to hold a socket. Anything else there is
// somebody else's file, and binding it would put it where the container looks
// for the service.
func TestSpawnRefusesAServiceSocketThatIsNotASocket(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cpak.sock")
	if err := os.WriteFile(source, []byte("not a socket"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, _, err := (&SpawnCmd{ServiceSocket: source}).mountServiceSocket(t.TempDir()); err == nil {
		t.Fatal("a path that is not a socket was bound as the service")
	}
}

func writeFakeCpak(t *testing.T, path, argsFile string, status int) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\n: > %[1]s\nfor arg in \"$@\"; do printf '%%s\\n' \"$arg\" >> %[1]s; done\necho nested-output\nexit %[2]d\n", argsFile, status)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write the fake cpak: %v", err)
	}
}

func waitForServiceSocket(t *testing.T, path string, served chan error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-served:
			t.Fatalf("the service stopped before it listened: %v", err)
		default:
		}
		conn, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the service never listened on %s", path)
}
