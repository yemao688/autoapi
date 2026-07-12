package metrics

import (
	"log/slog"
	"sync"
	"time"

	"autoapi/internal/model"
)

type SummarySink interface {
	UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary) error
}

type Checkpoint struct {
	registry *Registry
	sink     SummarySink
	interval time.Duration
	trigger  chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopDone chan struct{}
	mu       sync.Mutex
	flushMu  sync.Mutex
	state    checkpointState
}
type checkpointState uint8

const (
	checkpointNew checkpointState = iota
	checkpointStarted
	checkpointStopped
)

func NewCheckpoint(r *Registry, sink SummarySink, interval time.Duration) *Checkpoint {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Checkpoint{registry: r, sink: sink, interval: interval, trigger: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), stopDone: make(chan struct{})}
}

// Start returns false when already started or stopped.
func (c *Checkpoint) Start() bool {
	c.mu.Lock()
	if c.state != checkpointNew {
		c.mu.Unlock()
		return false
	}
	c.state = checkpointStarted
	c.mu.Unlock()
	go c.run()
	return true
}
func (c *Checkpoint) run() {
	defer close(c.done)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			c.Flush()
		case <-c.trigger:
			c.Flush()
		case <-c.stop:
			return
		}
	}
}

// Flush serializes all sink calls, including the final shutdown flush. A panic
// from a sink is contained so persistence cannot affect request serving.
func (c *Checkpoint) Flush() {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	defer func() {
		if v := recover(); v != nil {
			slog.Warn("metrics checkpoint panic", "value", v)
		}
	}()
	if c.sink == nil {
		return
	}
	if err := c.sink.UpsertTargetRuntimeSummaries(c.summaries()); err != nil {
		slog.Warn("metrics checkpoint failed", "err", err)
	}
}
func (c *Checkpoint) summaries() []model.TargetRuntimeSummary {
	now := time.Now().UTC()
	ss := c.registry.Snapshots(now)
	out := make([]model.TargetRuntimeSummary, 0, len(ss))
	for _, v := range ss {
		if !v.Key.Valid() {
			continue
		}
		out = append(out, model.TargetRuntimeSummary{Key: v.Key, Requests: v.Requests, Attempts: v.Attempts, Successes: v.Successes, Failures: v.Failures, Status429: v.Status429, Status5xx: v.Status5xx, Transport: v.Transport, ClientAborts: v.ClientAborts, Truncated: v.Truncated, Downstream: v.Downstream, LastUsed: v.LastUsed, LastSuccess: v.LastSuccess, LastFailure: v.LastFailure, UpdatedAt: now})
	}
	return out
}
func (c *Checkpoint) Trigger() {
	c.mu.Lock()
	active := c.state == checkpointStarted
	c.mu.Unlock()
	if !active {
		return
	}
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}
func (c *Checkpoint) Stop() {
	c.mu.Lock()
	switch c.state {
	case checkpointStopped:
		c.mu.Unlock()
		<-c.stopDone
		return
	case checkpointNew:
		c.state = checkpointStopped
		c.mu.Unlock()
		c.Flush()
		close(c.stopDone)
		return
	case checkpointStarted:
		c.state = checkpointStopped
		close(c.stop)
		c.mu.Unlock()
	}
	<-c.done
	// Flush is serialized after any in-flight ticker/trigger flush and captures
	// the latest snapshot, so it cannot be overwritten by an older flush.
	c.Flush()
	close(c.stopDone)
}
