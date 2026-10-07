package task

import (
	"sync"
	"time"
)

const workerIdleTimeout = 10 * time.Second

// maxIdleWorkers is guarded by workers.
var maxIdleWorkers = 4096

type worker struct {
	work      chan func()
	idleSince time.Time
}

var workers struct {
	sync.Mutex
	idle     []*worker // in the order they became idle
	cleaning bool
}

// Go runs f on an idle pooled goroutine, or on a new one if none is idle.
// Pooled goroutines keep the stacks they have grown, so short-lived tasks
// with deep call chains skip most of the stack growth a new goroutine pays.
func Go(f func()) {
	workers.Lock()
	if n := len(workers.idle); n > 0 {
		w := workers.idle[n-1]
		workers.idle[n-1] = nil
		workers.idle = workers.idle[:n-1]
		workers.Unlock()
		w.work <- f
		return
	}
	workers.Unlock()
	go (&worker{work: make(chan func(), 1)}).run(f)
}

func (w *worker) run(f func()) {
	for f != nil {
		f()
		f = nil
		if !w.park() {
			return
		}
		f = <-w.work
	}
}

// park adds w to the idle workers, or reports false if there are enough.
func (w *worker) park() bool {
	workers.Lock()
	defer workers.Unlock()
	if len(workers.idle) >= maxIdleWorkers {
		return false
	}
	w.idleSince = time.Now()
	workers.idle = append(workers.idle, w)
	if !workers.cleaning {
		workers.cleaning = true
		go stopIdleWorkers()
	}
	return true
}

// stopIdleWorkers stops workers idle for longer than workerIdleTimeout
// until none is left.
func stopIdleWorkers() {
	for {
		time.Sleep(workerIdleTimeout / 2)
		if stopWorkersIdleSince(time.Now().Add(-workerIdleTimeout)) == 0 {
			return
		}
	}
}

// stopWorkersIdleSince stops the workers that became idle before cutoff and
// returns the number of idle workers left.
func stopWorkersIdleSince(cutoff time.Time) int {
	workers.Lock()
	n := 0
	for n < len(workers.idle) && workers.idle[n].idleSince.Before(cutoff) {
		n++
	}
	stale := make([]*worker, n)
	copy(stale, workers.idle)
	kept := copy(workers.idle, workers.idle[n:])
	clear(workers.idle[kept:])
	workers.idle = workers.idle[:kept]
	if kept == 0 {
		workers.cleaning = false
	}
	workers.Unlock()

	for _, w := range stale {
		w.work <- nil
	}
	return kept
}
