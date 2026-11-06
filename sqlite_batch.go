package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
)

const (
	sqliteQueueCapacity = 64
	sqliteBatchSize     = 32
	sqliteBatchWait     = 2 * time.Millisecond
	sqliteOperationTTL  = 3 * time.Second
)

type sqliteOperation struct {
	ctx    context.Context
	fn     func(context.Context, *sql.Tx) error
	result chan error
}

// This volatile queue is admission control, not durable acceptance. Callers
// receive success only after COMMIT. Every operation waits until its callback
// finishes, so closures never mutate results after the caller has returned.
type sqliteBatcher struct {
	db                *sql.DB
	mu                sync.Mutex
	closed            bool
	queue             chan sqliteOperation
	done              chan struct{}
	rejected          atomic.Uint64
	beginFailures     atomic.Uint64
	operationFailures atomic.Uint64
	savepointFailures atomic.Uint64
	commitFailures    atomic.Uint64
	expired           atomic.Uint64
	busyErrors        atomic.Uint64
	ioErrors          atomic.Uint64
	lastIOCode        atomic.Int64
	interruptErrors   atomic.Uint64
	constraintErrors  atomic.Uint64
	otherErrors       atomic.Uint64
}

func (b *sqliteBatcher) classify(err error) {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case 5, 6:
			b.busyErrors.Add(1)
		case 9:
			b.interruptErrors.Add(1)
		case 10, 13:
			b.ioErrors.Add(1)
			b.lastIOCode.Store(int64(sqliteErr.Code()))
		case 19:
			b.constraintErrors.Add(1)
		default:
			b.otherErrors.Add(1)
		}
		return
	}
	b.otherErrors.Add(1)
}

func newSQLiteBatcher(db *sql.DB) *sqliteBatcher {
	b := &sqliteBatcher{db: db, queue: make(chan sqliteOperation, sqliteQueueCapacity), done: make(chan struct{})}
	go b.run()
	return b
}

func (b *sqliteBatcher) depth() int { return len(b.queue) }

func (b *sqliteBatcher) submit(fn func(context.Context, *sql.Tx) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), sqliteOperationTTL)
	defer cancel()
	op := sqliteOperation{ctx: ctx, fn: fn, result: make(chan error, 1)}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return storageError(errors.New("SQLite writer closed"))
	}
	select {
	case b.queue <- op:
		b.mu.Unlock()
	default:
		b.rejected.Add(1)
		b.mu.Unlock()
		return storageError(errors.New("SQLite writer queue full"))
	}
	return <-op.result
}

func (b *sqliteBatcher) close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.queue)
	}
	b.mu.Unlock()
	<-b.done
}

func (b *sqliteBatcher) run() {
	defer close(b.done)
	for first := range b.queue {
		batch := []sqliteOperation{first}
		timer := time.NewTimer(sqliteBatchWait)
	collect:
		for len(batch) < sqliteBatchSize {
			select {
			case op, ok := <-b.queue:
				if !ok {
					break collect
				}
				batch = append(batch, op)
			case <-timer.C:
				break collect
			}
		}
		timer.Stop()
		b.execute(batch)
	}
}

func (b *sqliteBatcher) execute(batch []sqliteOperation) {
	ctx, cancel := context.WithTimeout(context.Background(), sqliteOperationTTL)
	defer cancel()
	results := make([]error, len(batch))
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		b.classify(err)
		b.beginFailures.Add(1)
		b.finish(batch, results, storageError(err))
		return
	}
	defer func() { _ = tx.Rollback() }()
	for index, op := range batch {
		if err := op.ctx.Err(); err != nil {
			b.expired.Add(1)
			results[index] = storageError(err)
			continue
		}
		if _, err := tx.ExecContext(ctx, "SAVEPOINT batch_item"); err != nil {
			b.classify(err)
			b.savepointFailures.Add(1)
			b.finish(batch, results, storageError(err))
			return
		}
		results[index] = op.fn(ctx, tx)
		if results[index] != nil {
			if errors.Is(results[index], errStorageFailure) {
				b.classify(results[index])
				b.operationFailures.Add(1)
			}
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO batch_item"); err != nil {
				b.classify(err)
				b.savepointFailures.Add(1)
				b.finish(batch, results, storageError(err))
				return
			}
		}
		if _, err := tx.ExecContext(ctx, "RELEASE batch_item"); err != nil {
			b.classify(err)
			b.savepointFailures.Add(1)
			b.finish(batch, results, storageError(err))
			return
		}
	}
	commitErr := tx.Commit()
	if commitErr != nil {
		b.classify(commitErr)
		b.commitFailures.Add(1)
	}
	b.finish(batch, results, storageError(commitErr))
}

func (b *sqliteBatcher) finish(batch []sqliteOperation, results []error, batchErr error) {
	for index, op := range batch {
		if batchErr != nil {
			op.result <- batchErr
		} else {
			op.result <- results[index]
		}
	}
}
