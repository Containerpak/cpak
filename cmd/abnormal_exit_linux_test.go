/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestKilledApplicationTakesItsChildrenWithIt(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 60 & sleep 60 & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := command.Process.Pid
	time.Sleep(200 * time.Millisecond)
	_ = command.Process.Kill()
	err := command.Wait()
	if !abnormalExit(err, true) {
		t.Fatalf("a killed application was not seen as an abnormal exit: %v", err)
	}
	if syscall.Kill(-pgid, 0) != nil {
		t.Fatal("the children were already gone, the test proves nothing")
	}
	terminateProcessGroup(pgid, 2*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(-pgid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(-pgid, 0) == nil {
		t.Fatal("children of the killed application survived")
	}
}

func TestAbnormalExit(t *testing.T) {
	for _, c := range []struct {
		script     string
		countCodes bool
		want       bool
	}{
		{"exit 0", true, false},
		{"exit 1", true, false},
		{"exit 137", true, true},
		{"exit 130", false, false},
	} {
		err := exec.Command("sh", "-c", c.script).Run()
		if got := abnormalExit(err, c.countCodes); got != c.want {
			t.Errorf("%q counting codes %v: abnormal = %v", c.script, c.countCodes, got)
		}
	}
	if abnormalExit(nil, true) {
		t.Error("a clean exit is abnormal")
	}
}
