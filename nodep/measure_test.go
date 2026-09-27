package nodep

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const slowDial = 300 * time.Millisecond

// slowDialClient delays every new connection, standing in for proxy and TLS
// handshakes, and counts how many connections it opens.
func slowDialClient(dials *atomic.Int32, timeout int) *http.Client {
	dialer := &net.Dialer{}
	return &http.Client{
		Timeout: time.Second * time.Duration(timeout),
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				dials.Add(1)
				time.Sleep(slowDial)
				return dialer.DialContext(ctx, network, address)
			},
		},
	}
}

func TestPingHTTPRequestWarmExcludesConnectionSetup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var dials atomic.Int32

	delay, err := PingHTTPRequestWarm(slowDialClient(&dials, 3), server.URL, 3)

	if err != nil {
		t.Fatal(err)
	}
	if delay >= slowDial.Milliseconds() {
		t.Fatalf("delay = %dms, want the reused connection below %dms", delay, slowDial.Milliseconds())
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1 reused connection", dials.Load())
	}
}

func TestPingHTTPRequestWarmKeepsReachableServerWhenSecondRequestFails(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) > 1 {
			time.Sleep(2 * time.Second) // outlives the remaining timeout
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var dials atomic.Int32

	delay, err := PingHTTPRequestWarm(slowDialClient(&dials, 1), server.URL, 1)

	if err != nil {
		t.Fatalf("a reachable server must not fail: %v", err)
	}
	if delay < slowDial.Milliseconds() {
		t.Fatalf("delay = %dms, want the first request's %dms or more", delay, slowDial.Milliseconds())
	}
}

func TestPingHTTPRequestWarmReportsUnreachableServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()

	delay, err := PingHTTPRequestWarm(&http.Client{Timeout: time.Second}, "http://"+address, 1)

	if err == nil {
		t.Fatal("expected an error for a closed port")
	}
	if delay != PingDelayError {
		t.Fatalf("delay = %d, want %d", delay, PingDelayError)
	}
}
