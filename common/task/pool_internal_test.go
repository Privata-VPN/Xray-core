package task

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"time"
)

func goroutineID() string {
	b := make([]byte, 64)
	return string(bytes.Fields(b[:runtime.Stack(b, false)])[1])
}

func idleWorkers() int {
	workers.Lock()
	defer workers.Unlock()
	return len(workers.idle)
}

func TestGoReusesIdleWorker(t *testing.T) {
	ids := make(chan string)
	Go(func() { ids <- goroutineID() })
	first := <-ids

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		Go(func() { ids <- goroutineID() })
		if <-ids == first {
			return
		}
	}
	t.Fatal("tasks never ran on the goroutine of the first one")
}

func TestStopWorkersIdleSince(t *testing.T) {
	done := make(chan struct{})
	Go(func() { close(done) })
	<-done
	for idleWorkers() == 0 {
		time.Sleep(time.Millisecond)
	}

	if left := stopWorkersIdleSince(time.Now().Add(-time.Hour)); left == 0 {
		t.Fatal("stopped a worker that became idle after the cutoff")
	}
	if left := stopWorkersIdleSince(time.Now()); left != 0 {
		t.Fatalf("%v idle workers left, want 0", left)
	}
}

func TestMaxIdleWorkers(t *testing.T) {
	workers.Lock()
	maxIdleWorkers = 2
	workers.Unlock()
	defer func() {
		workers.Lock()
		maxIdleWorkers = 4096
		workers.Unlock()
	}()
	stopWorkersIdleSince(time.Now())

	var wg sync.WaitGroup
	release := make(chan struct{})
	for range 8 {
		wg.Add(1)
		Go(func() {
			defer wg.Done()
			<-release
		})
	}
	close(release)
	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	if n := idleWorkers(); n > 2 {
		t.Fatalf("%v idle workers, want at most 2", n)
	}
}
