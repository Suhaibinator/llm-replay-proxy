// replay-proxy records and replays inference traffic with an embedded control panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/local/llm-replay-proxy/internal/admin"
	"github.com/local/llm-replay-proxy/internal/config"
	"github.com/local/llm-replay-proxy/internal/proxy"
	"github.com/local/llm-replay-proxy/internal/store"
	"github.com/local/llm-replay-proxy/web"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", "", "optional server JSON configuration")
	listen := flag.String("listen", "", "override bind address (default 127.0.0.1:8080)")
	database := flag.String("db", "", "override SQLite database path")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := config.LoadWithOverrides(ctx, *configPath, config.Overrides{Listen: *listen, Database: *database})
	if err != nil {
		return err
	}
	db, err := store.Open(c.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	upstreams := make(map[string]proxy.Upstream, len(c.Upstreams))
	for route, u := range c.Upstreams {
		upstreams[route] = proxy.Upstream{URL: u.URL, APIKey: u.APIKey, Identity: u.Identity, Headers: u.Headers}
	}
	mux := http.NewServeMux()
	inference := proxy.New(db, proxy.Config{Upstreams: upstreams})
	for _, route := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		mux.Handle(route, inference)
	}
	mux.Handle("/api/", admin.New(db))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", web.Handler())
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Printf("Replay proxy listening on http://%s (database %s)", listener.Addr(), c.Database)
	// serve returns only after every handler has finished, so the deferred
	// db.Close cannot race history writes.
	return serve(ctx, stop, listener, mux)
}

// Bounds for shutdown: how long in-flight requests get to finish after their
// contexts are canceled, and how long to wait for handlers after connections
// are force-closed. Variables so tests can shorten them.
var (
	shutdownTimeout = 10 * time.Second
	handlerDrain    = 5 * time.Second
)

// serve runs the HTTP server on listener until ctx is done, then shuts down.
// stop releases the signal handler so a second interrupt terminates at once.
func serve(ctx context.Context, stop context.CancelFunc, listener net.Listener, handler http.Handler) error {
	// Shutdown does not cancel request contexts by itself; open streams,
	// upstream requests and replay delays observe this context instead.
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	var active atomic.Int64
	tracked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler: tracked, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return requests },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop()
	log.Print("Shutting down; interrupt again to exit immediately")
	cancelRequests()
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		log.Printf("Graceful shutdown incomplete (%v); closing connections", err)
		_ = server.Close()
	}
	<-done
	// Close does not wait for handlers. They have been canceled and normally
	// return at once; this bound keeps a stuck handler from blocking exit.
	deadline := time.Now().Add(handlerDrain)
	for active.Load() > 0 {
		if time.Now().After(deadline) {
			log.Printf("%d handlers still running after shutdown", active.Load())
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}
