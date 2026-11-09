package background

import (
	"os"
	"strconv"
	"syscall"
	"testing"
)

func TestEnterLowersEveryThread(t *testing.T) {
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() { <-done }()
	}
	Enter()
	close(done)
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Skip("no /proc")
	}
	for _, entry := range entries {
		tid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		prio, err := syscall.Getpriority(syscall.PRIO_PROCESS, tid)
		if err != nil {
			t.Fatal(err)
		}
		if nice := 20 - prio; nice != backgroundNice {
			t.Fatalf("thread %d at nice %d", tid, nice)
		}
		class, _, errno := syscall.Syscall(syscall.SYS_IOPRIO_GET, ioprioWhoProcess, uintptr(tid), 0)
		if errno != 0 {
			t.Fatal(errno)
		}
		if class>>ioprioClassShift != ioprioClassIdle {
			t.Fatalf("thread %d disk class %d", tid, class>>ioprioClassShift)
		}
	}
}
