package service

import (
	"testing"

	"autoapi/internal/store"
)

func TestGetSystemHealth(t *testing.T) {
	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	svc := New(st, nil, t.TempDir())
	h, err := svc.GetSystemHealth()
	if err != nil {
		t.Fatalf("GetSystemHealth: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil health")
	}
	if h.Status != "paused" {
		t.Errorf("expected status paused, got %q", h.Status)
	}
	if h.UptimeSeconds < 0 {
		t.Errorf("expected uptime >= 0, got %d", h.UptimeSeconds)
	}
	if h.MemoryMB < 0 {
		t.Errorf("expected memory >= 0, got %d", h.MemoryMB)
	}
	if h.CPUPercent < 0 {
		t.Errorf("expected cpu percent >= 0, got %f", h.CPUPercent)
	}
}

// fakeProxy implements the proxy surface needed by Service.
type fakeProxy struct{}

func (fakeProxy) IsRunning() bool { return true }
func (fakeProxy) URL() string     { return "http://127.0.0.1:9999" }
func (fakeProxy) ActiveConnections() int { return 7 }

func TestGetSystemHealthWithProxy(t *testing.T) {
	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	svc := New(st, fakeProxy{}, t.TempDir())
	h, err := svc.GetSystemHealth()
	if err != nil {
		t.Fatalf("GetSystemHealth: %v", err)
	}
	if h.Status != "running" {
		t.Errorf("expected status running, got %q", h.Status)
	}
	if h.ProxyURL != "http://127.0.0.1:9999" {
		t.Errorf("expected proxy url, got %q", h.ProxyURL)
	}
	if h.ActiveConnections != 7 {
		t.Errorf("expected active connections 7, got %d", h.ActiveConnections)
	}
}
