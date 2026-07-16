package metrics

import (
	"autoapi/internal/model"
	"errors"
	"sync"
	"testing"
	"time"
)

type checkpointSink struct {
	mu      sync.Mutex
	calls   int
	active  int
	max     int
	entered chan struct{}
	release chan struct{}
	panic   bool
	err     bool
}

func (s *checkpointSink) UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary) error {
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.max {
		s.max = s.active
	}
	if s.entered != nil {
		close(s.entered)
		s.entered = nil
	}
	release := s.release
	pan := s.panic
	fail := s.err
	s.mu.Unlock()
	if release != nil {
		<-release
	}
	if pan {
		panic("sink panic")
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	if fail {
		return errors.New("sink")
	}
	return nil
}
func (s *checkpointSink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }
func (s *checkpointSink) enteredSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entered
}
func checkpointRegistry() *Registry {
	r := New(4, time.Hour)
	r.Submit(model.TargetMetricEvent{Key: model.TargetMetricKey{TargetID: "t", ProviderID: "p", ModelName: "m", Endpoint: "e"}, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: time.Now()})
	return r
}

func TestCheckpointLifecycleAndSerializedFlush(t *testing.T) {
	s := &checkpointSink{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewCheckpoint(checkpointRegistry(), s, time.Hour)
	if !c.Start() {
		t.Fatal("first start")
	}
	go c.Flush()
	entered := s.enteredSignal()
	if entered == nil {
		t.Fatal("flush did not expose entry signal")
	}
	<-entered
	done := make(chan struct{})
	go func() { c.Stop(); close(done) }()
	select {
	case <-done:
		t.Fatal("stop did not wait")
	default:
	}
	close(s.release)
	<-done
	s.mu.Lock()
	max := s.max
	s.mu.Unlock()
	if max != 1 {
		t.Fatalf("max concurrent flush=%d", max)
	}
	if !c.Start() {
	} else {
		t.Fatal("restart succeeded")
	}
	c.Stop()
}
func TestCheckpointStopBeforeStartAndSinkPanic(t *testing.T) {
	s := &checkpointSink{panic: true}
	c := NewCheckpoint(checkpointRegistry(), s, time.Hour)
	c.Stop()
	if c.Start() {
		t.Fatal("start after stop")
	}
	c.Stop()
	p := NewCheckpoint(checkpointRegistry(), s, time.Hour)
	p.Start()
	p.Trigger()
	p.Stop()
}
func TestCheckpointTriggerCoalesces(t *testing.T) {
	s := &checkpointSink{}
	c := NewCheckpoint(checkpointRegistry(), s, time.Hour)
	c.Start()
	for i := 0; i < 10; i++ {
		c.Trigger()
	}
	c.Stop()
	if s.count() < 1 {
		t.Fatal("no flush")
	}
}
