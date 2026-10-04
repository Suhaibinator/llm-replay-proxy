package main

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// startServe runs serve on a loopback listener and returns the base URL, the
// shutdown trigger, whether stop was called, and serve's result channel.
func startServe(t *testing.T, handler http.Handler) (string, context.CancelFunc, *atomic.Bool, chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopped := &atomic.Bool{}
	result := make(chan error, 1)
	go func() { result <- serve(ctx, func() { stopped.Store(true) }, listener, handler) }()
	return "http://" + listener.Addr().String(), cancel, stopped, result
}

// openStream issues a request and returns once response headers arrive.
func openStream(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
}

func TestShutdownCancelsOpenStreamsAndWaitsForHandlers(t *testing.T) {
	var finished atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		time.Sleep(50 * time.Millisecond) // e.g. writing the history entry
		finished.Store(true)
	})
	url, shutdown, stopped, result := startServe(t, handler)
	openStream(t, url)
	started := time.Now()
	shutdown()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("open stream kept the server from shutting down")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
	if !finished.Load() {
		t.Fatal("serve returned before the handler finished")
	}
	if !stopped.Load() {
		t.Fatal("signal handling was not released after the first signal")
	}
}

func TestShutdownTimeoutIsACleanExit(t *testing.T) {
	oldTimeout := shutdownTimeout
	shutdownTimeout = 50 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = oldTimeout })
	var finished atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond) // ignores cancellation
		finished.Store(true)
	})
	url, shutdown, _, result := startServe(t, handler)
	openStream(t, url)
	shutdown()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("timed-out shutdown returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return")
	}
	if !finished.Load() {
		t.Fatal("serve returned while a handler was still running")
	}
}
