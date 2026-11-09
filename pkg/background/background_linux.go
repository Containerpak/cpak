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
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		lowerTask(0)
		return
	}
	for _, entry := range entries {
		if tid, err := strconv.Atoi(entry.Name()); err == nil {
			lowerTask(tid)
		}
	}
}

func EnterUnlessFollowed() {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	Enter()
}
