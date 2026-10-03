// replay-proxy records and replays inference traffic with an embedded control panel.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	server := &http.Server{Addr: c.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("Replay proxy listening on http://%s (database %s)", c.Listen, c.Database)
	select {
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
		return err
	}
	return nil
}
