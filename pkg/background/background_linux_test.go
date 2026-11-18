package background

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

func TestEnterLowersEveryThread(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	ready := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ready <- struct{}{}
			<-done
		}()
	}
	for i := 0; i < 4; i++ {
		<-ready
	}
	Enter()
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
