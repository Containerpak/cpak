package cpak

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func x11WaitListener(t *testing.T) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "X")
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := unix.Listen(fd, 1); err != nil {
		t.Fatal(err)
	}
	return fd, path
}

func x11WaitContainer(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func x11WaitResult(t *testing.T, ch <-chan bool, want bool) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("pending = %v, want %v", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not return")
	}
}

func TestWaitForX11ClientReturnsWhenContainerExits(t *testing.T) {
	fd, _ := x11WaitListener(t)
	container := x11WaitContainer(t)
	ch := make(chan bool, 1)
	go func() {
		pending, _ := waitForX11Client(context.Background(), fd, container.Process.Pid, func() bool { return true })
		ch <- pending
	}()
	time.Sleep(50 * time.Millisecond)
	_ = container.Process.Kill()
	x11WaitResult(t, ch, false)
}

func TestWaitForX11ClientReturnsOnCancel(t *testing.T) {
	fd, _ := x11WaitListener(t)
	container := x11WaitContainer(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan bool, 1)
	go func() {
		pending, _ := waitForX11Client(ctx, fd, container.Process.Pid, func() bool { return true })
		ch <- pending
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	x11WaitResult(t, ch, false)
}

func TestWaitForX11ClientReportsAClient(t *testing.T) {
	fd, path := x11WaitListener(t)
	container := x11WaitContainer(t)
	ch := make(chan bool, 1)
	go func() {
		pending, _ := waitForX11Client(context.Background(), fd, container.Process.Pid, func() bool { return true })
		ch <- pending
	}()
	time.Sleep(50 * time.Millisecond)
	client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(client)
	if err := unix.Connect(client, &unix.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	x11WaitResult(t, ch, true)
}
