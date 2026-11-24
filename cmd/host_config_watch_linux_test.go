/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func hostViewWorkerForTest(t *testing.T) *grantMountWorker {
	worker := &grantMountWorker{requests: make(chan grantMountRequest), done: make(chan struct{})}
	go func() {
		defer close(worker.done)
		for request := range worker.requests {
			request.result <- grantMountResult{err: request.run()}
		}
	}()
	t.Cleanup(worker.Close)
	return worker
}

func waitForContent(t *testing.T, path, expected string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		current, err := os.ReadFile(path)
		if err == nil && string(current) == expected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s holds %q, want %q", path, current, expected)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFollowHostConfiguration(t *testing.T) {
	host := t.TempDir()
	resolver := filepath.Join(host, "resolv.conf")
	hosts := filepath.Join(host, "hosts")
	if err := os.WriteFile(resolver, []byte("nameserver 10.20.10.1\nsearch office.lan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sandbox := t.TempDir()
	followed := []followedConfiguration{}
	for _, source := range []string{resolver, hosts} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		local := filepath.Join(sandbox, filepath.Base(source))
		if err = os.WriteFile(local, content, 0644); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(local, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { file.Close() })
		followed = append(followed, followedConfiguration{source: source, local: file, content: content})
	}
	localResolver := filepath.Join(sandbox, "resolv.conf")
	localHosts := filepath.Join(sandbox, "hosts")
	before, err := os.Stat(localResolver)
	if err != nil {
		t.Fatal(err)
	}

	spawn := &SpawnCmd{followedConfiguration: followed}
	go spawn.followHostConfiguration(hostViewWorkerForTest(t))

	replacement := filepath.Join(host, ".resolv.conf.new")
	if err = os.WriteFile(replacement, []byte("nameserver 192.168.1.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, resolver); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, localResolver, "nameserver 192.168.1.1\n", 2*time.Second)

	if err = os.WriteFile(hosts, []byte("127.0.0.1 localhost\n192.0.2.77 printer.lan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, localHosts, "127.0.0.1 localhost\n192.0.2.77 printer.lan\n", 2*time.Second)

	after, err := os.Stat(localResolver)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("the sandbox copy was replaced instead of rewritten in place")
	}

	target := filepath.Join(t.TempDir(), "stub-resolv.conf")
	if err = os.WriteFile(target, []byte("nameserver 127.0.0.53\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(resolver); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, resolver); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, localResolver, "nameserver 127.0.0.53\n", 2*time.Second)
	if err = os.WriteFile(target, []byte("nameserver 9.9.9.9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	waitForContent(t, localResolver, "nameserver 9.9.9.9\n", 2*time.Second)
}

func TestFollowsHost(t *testing.T) {
	cases := []struct {
		conf, nameserver string
		want             bool
	}{
		{"/etc/resolv.conf", "", true},
		{"/etc/resolv.conf", "10.0.2.3", false},
		{"/etc/hosts", "", true},
		{"/etc/hosts", "10.0.2.3", true},
		{"/etc/passwd", "", false},
	}
	for _, c := range cases {
		if got := followsHost(c.conf, c.nameserver); got != c.want {
			t.Errorf("followsHost(%q, %q) = %v, want %v", c.conf, c.nameserver, got, c.want)
		}
	}
}
