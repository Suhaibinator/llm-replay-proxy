// Command replay-seed fills a replay-proxy database with synthetic traffic for
// console development:
//
//	go run ./cmd/replay-seed -db dev.sqlite [-rows 4000] [-days 30] [-seed 1] [-name "Demo traffic"] [-activate=true]
//
// It creates a new collection each run and never touches existing ones.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/local/llm-replay-proxy/internal/devseed"
	"github.com/local/llm-replay-proxy/internal/store"
)

func main() {
	path := flag.String("db", "", "SQLite database to fill (created if missing)")
	rows := flag.Int("rows", 4000, "approximate number of history rows")
	days := flag.Int("days", 30, "days of traffic before now")
	seed := flag.Uint64("seed", 1, "random seed; the same seed yields the same traffic")
	name := flag.String("name", "Demo traffic", "collection name")
	activate := flag.Bool("activate", true, "make the new collection the active one")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: replay-seed -db path [-rows n] [-days n] [-seed n] [-name s] [-activate=false]")
		os.Exit(2)
	}
	db, err := store.Open(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open database:", err)
		os.Exit(1)
	}
	defer db.Close()
	started := time.Now()
	res, err := devseed.Seed(context.Background(), db, devseed.Options{Collection: *name, Rows: *rows, Days: *days, Seed: *seed, Activate: *activate})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("collection %d %q: %d recordings, %d revisions, %d history rows in %v\n",
		res.Collection.ID, res.Collection.Name, res.Recordings, res.Revisions, res.History, time.Since(started).Round(time.Millisecond))
}
