package service

import (
	"strings"
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
	// APIAddress should reuse the proxy's port (9999) and a valid IPv4 host.
	if h.APIAddress == "" {
		t.Fatalf("expected non-empty api address when proxy is running")
	}
	if !strings.HasSuffix(h.APIAddress, ":9999") {
		t.Errorf("expected api address to end with :9999, got %q", h.APIAddress)
	}
	if !strings.HasPrefix(h.APIAddress, "http://") {
		t.Errorf("expected api address to start with http://, got %q", h.APIAddress)
	}
}

// noProxy implements the proxy surface but reports not running.
type noProxy struct{}

func (noProxy) IsRunning() bool         { return false }
func (noProxy) URL() string             { return "http://0.0.0.0:8344" }
func (noProxy) ActiveConnections() int { return 0 }

func TestGetSystemHealthAPIAddressWhenProxyDown(t *testing.T) {
	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	svc := New(st, noProxy{}, t.TempDir())
	h, err := svc.GetSystemHealth()
	if err != nil {
		t.Fatalf("GetSystemHealth: %v", err)
	}
	if h.APIAddress != "" {
		t.Errorf("expected empty api address when proxy is not running, got %q", h.APIAddress)
	}
}

func TestResolveAPIAddress(t *testing.T) {
	cases := []struct {
		name    string
		proxy   string
		wantPre string // prefix check, leaving IPv4 host open
		wantSuf string // port suffix
		wantErr bool   // true if we expect empty result
	}{
		{"scheme and IPv4 bind", "http://0.0.0.0:8344", "http://", ":8344", false},
		{"no scheme IPv4 bind", "0.0.0.0:9999", "http://", ":9999", false},
		{"IPv6 bind", "http://[::]:8123", "http://", ":8123", false},
		{"localhost", "http://127.0.0.1:4000", "http://", ":4000", false},
		{"empty", "", "", "", true},
		{"no port", "http://0.0.0.0", "", "", true},
		{"malformed", "not-a-url", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveAPIAddress(c.proxy)
			if c.wantErr {
				if got != "" {
					t.Errorf("expected empty result, got %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, c.wantPre) || !strings.HasSuffix(got, c.wantSuf) {
				t.Errorf("resolveAPIAddress(%q) = %q, want prefix %q and suffix %q", c.proxy, got, c.wantPre, c.wantSuf)
			}
		})
	}
}

func TestLocalIPv4ReturnsValid(t *testing.T) {
	addr := localIPv4()
	// Must always be a parseable IPv4 dotted-quad (127.0.0.1 is acceptable on
	// IPv6-only or no-network environments).
	if addr == "" {
		t.Fatal("localIPv4 returned empty")
	}
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		t.Fatalf("expected 4 dotted parts, got %q", addr)
	}
	// Must not be link-local (169.254.x.x) — those are filtered out.
	if strings.HasPrefix(addr, "169.254.") {
		t.Errorf("localIPv4 should not return link-local address, got %q", addr)
	}
	// Must not be loopback unless that is all we have (which is allowed).
	for _, p := range parts {
		if p == "" {
			t.Errorf("empty part in %q", addr)
		}
	}
}
