package background

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/term"
)

const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
	backgroundNice   = 19
)

func lowerTask(tid int) {
	syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), ioprioClassIdle<<ioprioClassShift)
	syscall.Setpriority(syscall.PRIO_PROCESS, tid, backgroundNice)
}

func Enter() {
	seen := make(map[string]bool)
	for {
		entries, err := os.ReadDir("/proc/self/task")
		if err != nil {
			lowerTask(0)
			return
		}
		updated := false
		for _, entry := range entries {
			if seen[entry.Name()] {
				continue
			}
			if tid, err := strconv.Atoi(entry.Name()); err == nil {
				lowerTask(tid)
				seen[entry.Name()] = true
				updated = true
			}
		}
		// Threads created during the first pass can inherit the old priority.
		// Once a pass finds no new threads, their parents have all been lowered.
		if !updated {
			return
		}
	}
}

func EnterUnlessFollowed() {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	Enter()
}
