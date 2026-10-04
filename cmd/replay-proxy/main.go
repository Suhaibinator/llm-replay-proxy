// replay-proxy records and replays inference traffic with an embedded control panel.
//
// Usage:
//
//	replay-proxy [-config path] [-listen addr] [-db path]          start the server
//	replay-proxy token issue [-config path] [-db path] [-ttl d] [-subject s]
//	                                                                  print a new access token
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/local/llm-replay-proxy/internal/admin"
	"github.com/local/llm-replay-proxy/internal/auth"
	"github.com/local/llm-replay-proxy/internal/config"
	"github.com/local/llm-replay-proxy/internal/proxy"
	"github.com/local/llm-replay-proxy/internal/store"
	"github.com/local/llm-replay-proxy/web"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "token" {
		err = tokenCommand(context.Background(), os.Args[2:], os.Stdout, os.Stderr)
	} else {
		err = run()
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "optional server JSON configuration")
	listen := flag.String("listen", "", "override bind address (default 127.0.0.1:8080)")
	database := flag.String("db", "", "override SQLite database path")
	flag.Parse()
	if flag.NArg() > 0 {
		return fmt.Errorf("unknown command %q (use `replay-proxy token issue` to issue an access token)", flag.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := config.LoadWithOverrides(ctx, *configPath, config.Overrides{Listen: *listen, Database: *database})
	if err != nil {
		return err
	}
	authority, err := authorityFor(c, log.Printf)
	if err != nil {
		return err
	}
	if authority != nil {
		source := c.Auth.SigningKeySource
		if source == "" {
			source = "file " + auth.DefaultKeyPath(c.Database)
		}
		log.Printf("Client authentication enabled (JWT signing key from %s); issue tokens with `replay-proxy token issue`", source)
	}
	db, err := store.Open(c.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	server := &http.Server{Addr: c.Listen, Handler: newHandler(db, c, authority), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
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

// newHandler wires every route. A nil authority means auth.disabled (only
// accepted for loopback listeners by config validation).
func newHandler(db *store.Store, c config.Config, authority *auth.Authority) http.Handler {
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
	if authority == nil {
		return mux
	}
	return authority.Middleware(mux)
}

// authorityFor resolves the signing key: REPLAY_JWT_KEY, a configured key
// file or KMS secret, or else a key generated once next to the database.
func authorityFor(c config.Config, logf func(string, ...any)) (*auth.Authority, error) {
	if c.Auth.Disabled {
		logf("WARNING: client authentication is disabled (auth.disabled); anyone who can reach %s can use the proxy", c.Listen)
		return nil, nil
	}
	key := c.Auth.SigningKey
	if key.IsZero() {
		path := auth.DefaultKeyPath(c.Database)
		var created bool
		var err error
		key, created, err = auth.LoadOrCreateKeyFile(path)
		if err != nil {
			return nil, err
		}
		if created {
			logf("Generated a new JWT signing key at %s; issue client tokens with `replay-proxy token issue -db %s`", path, c.Database)
		}
	}
	return auth.New(key)
}

func tokenCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	usage := func() {
		fmt.Fprintln(stderr, "usage: replay-proxy token issue [-config path] [-db path] [-listen addr] [-ttl duration] [-subject name]")
	}
	if len(args) == 0 || args[0] != "issue" {
		usage()
		if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help") {
			return flag.ErrHelp
		}
		return errors.New("unknown token command")
	}
	fs := flag.NewFlagSet("replay-proxy token issue", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "server JSON configuration (same as the server's)")
	database := fs.String("db", "", "override SQLite database path (locates the generated signing key)")
	listen := fs.String("listen", "", "override listen address used for the printed console link")
	ttl := fs.Duration("ttl", auth.DefaultTTL, "token lifetime; 0 issues a token that never expires")
	subject := fs.String("subject", auth.DefaultSubject, "subject (sub claim) identifying the client")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *ttl < 0 {
		return errors.New("-ttl must not be negative")
	}
	c, err := config.LoadWithOverrides(ctx, *configPath, config.Overrides{Listen: *listen, Database: *database})
	if err != nil {
		return err
	}
	if c.Auth.Disabled {
		return errors.New("client authentication is disabled in this configuration (auth.disabled); tokens are not used")
	}
	authority, err := authorityFor(c, func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) })
	if err != nil {
		return err
	}
	token, claims, err := authority.Issue(*subject, *ttl)
	if err != nil {
		return err
	}
	// Only the token goes to stdout so `TOKEN=$(replay-proxy token issue)` works.
	fmt.Fprintln(stdout, token)
	expiry := "never (valid until the signing key is rotated)"
	if !claims.ExpiresAt.IsZero() {
		expiry = claims.ExpiresAt.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(stderr, "Subject: %s\nExpires: %s\nConsole login link: %s\n", claims.Subject, expiry, consoleURL(c.Listen, token))
	return nil
}

// consoleURL builds the login link for the configured listen address. A
// wildcard bind is shown as the matching loopback address.
func consoleURL(listen, token string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "127.0.0.1", "8080"
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: auth.LoginPath, RawQuery: url.Values{"token": {token}}.Encode()}
	return u.String()
}
