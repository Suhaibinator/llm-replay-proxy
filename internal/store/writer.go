package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// The proxy's own writes (publications and history rows) go through one
// writer goroutine that commits whatever is queued together, in one
// transaction (group commit). Under load a single commit, and so a single
// fsync and one round of file locks, covers many calls instead of each call
// paying its own. Every caller still waits for the commit that holds its
// write, so a recording is visible to the next lookup, and durable, before
// Publish returns.
//
// Each write runs inside its own savepoint, so one failing write leaves the
// others in its batch untouched.

// errClosed rejects writes after Close.
var errClosed = errors.New("store: closed")

// maxBatch bounds the writes committed together; a variable for the load test.
var maxBatch = 64

// durablePublish makes every publication sync to disk before it returns. It
// is a variable only so the load test can measure what it costs.
var durablePublish = true

// Write states: a cancellable write is skipped if its caller gave up before
// the writer reached it, and runs to completion once started.
const (
	opPending int32 = iota
	opRunning
	opAbandoned
)

type writeOp struct {
	fn func(ctx context.Context, tx *sql.Tx) error
	// durable writes are synced to disk by their commit (synchronous=FULL);
	// a batch without one commits with synchronous=NORMAL, which WAL keeps
	// consistent and the next durable commit or checkpoint syncs.
	durable bool
	// cancellable writes are skipped when their caller's context ends
	// before they start. Others always run once queued.
	cancellable bool
	state       atomic.Int32
	done        chan error
}

type writer struct {
	db     *sql.DB
	mu     sync.RWMutex
	closed bool
	ops    chan *writeOp
	exited chan struct{}
	// commits and writes count transactions and the writes they held.
	commits, writes, busy atomic.Int64 // busy: nanoseconds spent in transactions
}

func newWriter(db *sql.DB) *writer {
	w := &writer{db: db, ops: make(chan *writeOp, 1024), exited: make(chan struct{})}
	go w.loop()
	return w
}

// enqueue queues op, or reports errClosed. With wait false it never blocks:
// a full queue drops op and reports false.
func (w *writer) enqueue(op *writeOp, wait bool) (bool, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return false, errClosed
	}
	if wait {
		w.ops <- op
		return true, nil
	}
	select {
	case w.ops <- op:
		return true, nil
	default:
		return false, nil
	}
}

// write runs fn in the writer's next transaction and returns once that
// transaction has committed (or fn failed). fn must use only tx, never the
// store's read pool.
func (w *writer) write(ctx context.Context, durable, cancellable bool, fn func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op := &writeOp{fn: fn, durable: durable, cancellable: cancellable, done: make(chan error, 1)}
	if _, err := w.enqueue(op, true); err != nil {
		return err
	}
	select {
	case err := <-op.done:
		return err
	case <-ctx.Done():
	}
	if cancellable && op.state.CompareAndSwap(opPending, opAbandoned) {
		return ctx.Err()
	}
	if !cancellable {
		// The write still happens; the caller only stops waiting for it.
		return ctx.Err()
	}
	// Already running: its outcome is decided, so report it.
	return <-op.done
}

// writeLater queues fn without waiting or blocking, for derived data a
// read computed. A full queue or a closed store drops it.
func (w *writer) writeLater(fn func(context.Context, *sql.Tx) error) {
	_, _ = w.enqueue(&writeOp{fn: fn, done: make(chan error, 1)}, false)
}

// close stops accepting writes and returns once every queued one has been
// committed.
func (w *writer) close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ops)
	}
	w.mu.Unlock()
	<-w.exited
}

func (w *writer) loop() {
	defer close(w.exited)
	for op := range w.ops {
		batch := []*writeOp{op}
	collect:
		for len(batch) < maxBatch {
			select {
			case next, ok := <-w.ops:
				if !ok {
					break collect
				}
				batch = append(batch, next)
			default:
				break collect
			}
		}
		w.commit(batch)
	}
}

// commit runs batch in one transaction and reports each write's outcome.
func (w *writer) commit(batch []*writeOp) {
	run := batch[:0:0]
	durable := false
	for _, op := range batch {
		if op.cancellable && !op.state.CompareAndSwap(opPending, opRunning) {
			continue // abandoned by its caller
		}
		op.state.Store(opRunning)
		run = append(run, op)
		durable = durable || op.durable
	}
	if len(run) == 0 {
		return
	}
	w.commits.Add(1)
	w.writes.Add(int64(len(run)))
	// Statements use the writer's own context: interrupting one statement
	// inside an explicit transaction can roll back the whole transaction.
	ctx := context.Background()
	results := make([]error, len(run))
	started := time.Now()
	defer func() { w.busy.Add(int64(time.Since(started))) }()
	err := w.transact(ctx, durable, func(tx *sql.Tx) error {
		for i, op := range run {
			if _, err := tx.ExecContext(ctx, "SAVEPOINT op"); err != nil {
				return err
			}
			results[i] = op.fn(ctx, tx)
			if results[i] != nil {
				if _, err := tx.ExecContext(ctx, "ROLLBACK TO op"); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "RELEASE op"); err != nil {
				return err
			}
		}
		return nil
	})
	for i, op := range run {
		if err != nil {
			results[i] = err
		}
		op.done <- results[i]
	}
}

// transact runs fn in a transaction on the writer connection, committing
// with synchronous=NORMAL unless durable.
func (w *writer) transact(ctx context.Context, durable bool, fn func(*sql.Tx) error) error {
	conn, err := w.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if !durable {
		if _, err = conn.ExecContext(ctx, "PRAGMA synchronous=NORMAL"); err != nil {
			return err
		}
		defer func() {
			if _, e := conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); e != nil {
				// A connection left at NORMAL would weaken every later write
				// that is meant to be durable; drop it from the pool instead.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				log.Printf("store: restore synchronous=FULL: %v", e)
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// checkpointInterval is how often the WAL is checkpointed in the background.
const checkpointInterval = time.Second

// checkpoints copies committed WAL pages into the database file from a
// connection of its own until ctx ends, so the writer never stalls on a
// checkpoint and its fsyncs. PASSIVE never waits for readers or the writer.
func checkpoints(ctx context.Context, db *sql.DB, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(checkpointInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil && ctx.Err() == nil {
			log.Printf("store: checkpoint: %v", err)
		}
	}
}
