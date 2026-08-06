package worker

import (
	"sync"
	"testing"
	"time"
)

func TestHealthProbesRunConcurrently(t *testing.T) {
	const probeCount = 5
	started := make(chan struct{}, probeCount)
	release := make(chan struct{})
	done := make(chan struct{})
	var closeOnce sync.Once
	closeRelease := func() { closeOnce.Do(func() { close(release) }) }
	defer closeRelease()

	probes := make([]func(), probeCount)
	for index := range probes {
		probes[index] = func() {
			started <- struct{}{}
			<-release
		}
	}
	go func() {
		runHealthProbes(probes...)
		close(done)
	}()

	for index := 0; index < probeCount; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only %d of %d health probes started before another probe finished", index, probeCount)
		}
	}
	closeRelease()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent health probes did not finish")
	}
}
