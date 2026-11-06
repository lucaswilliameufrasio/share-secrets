package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func batchOperation(fn func(context.Context, *sql.Tx) error) sqliteOperation {
	return sqliteOperation{ctx: context.Background(), fn: fn, result: make(chan error, 1)}
}

func TestSQLiteBatchSavepointsIsolateDomainFailures(t *testing.T) {
	store, err := openSQLiteStore(migratedSQLite(t), time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.close() }()
	if _, err := store.db.Exec("CREATE TABLE batch_test (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	domainErr := errors.New("synthetic validation failure")
	good := batchOperation(func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO batch_test VALUES (1)")
		return err
	})
	bad := batchOperation(func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO batch_test VALUES (2)"); err != nil {
			return err
		}
		return domainErr
	})
	store.batch.execute([]sqliteOperation{good, bad})
	if err := <-good.result; err != nil {
		t.Fatalf("good operation failed: %v", err)
	}
	if err := <-bad.result; !errors.Is(err, domainErr) {
		t.Fatalf("domain failure lost: %v", err)
	}
	var count, sum int
	if err := store.db.QueryRow("SELECT count(*), sum(value) FROM batch_test").Scan(&count, &sum); err != nil || count != 1 || sum != 1 {
		t.Fatalf("savepoint isolation count=%d sum=%d err=%v", count, sum, err)
	}
}

func TestSQLiteBatchCommitFailureDoesNotAcknowledgeAnyOperation(t *testing.T) {
	store, err := openSQLiteStore(migratedSQLite(t), time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.close() }()
	bad := batchOperation(func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO payloads(id,ciphertext,iv,proof_hash) VALUES('missing','x','x',?)", make([]byte, 32))
		return err
	})
	good := batchOperation(func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE store_counters SET created=created+1 WHERE singleton=1")
		return err
	})
	store.batch.execute([]sqliteOperation{bad, good})
	for _, op := range []sqliteOperation{bad, good} {
		if err := <-op.result; !errors.Is(err, errStorageFailure) {
			t.Fatalf("failed commit acknowledged an operation: %v", err)
		}
	}
	var count int
	if err := store.db.QueryRow("SELECT created FROM store_counters WHERE singleton=1").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed batch was not rolled back: count=%d err=%v", count, err)
	}
}

func TestSQLiteBatchSkipsExpiredOperation(t *testing.T) {
	store, err := openSQLiteStore(migratedSQLite(t), time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executed := false
	op := sqliteOperation{ctx: ctx, result: make(chan error, 1), fn: func(context.Context, *sql.Tx) error {
		executed = true
		return nil
	}}
	store.batch.execute([]sqliteOperation{op})
	if err := <-op.result; !errors.Is(err, errStorageFailure) || executed {
		t.Fatalf("expired operation executed=%t err=%v", executed, err)
	}
}

func TestSQLiteBatchBoundedAdmissionAndDrain(t *testing.T) {
	store, err := openSQLiteStore(migratedSQLite(t), time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var workers sync.WaitGroup
	workers.Go(func() {
		if err := store.transaction(func(context.Context, *sql.Tx) error {
			close(started)
			<-release
			return nil
		}); err != nil {
			t.Errorf("blocked operation: %v", err)
		}
	})
	<-started
	for i := 0; i < sqliteQueueCapacity; i++ {
		workers.Go(func() {
			if err := store.transaction(func(context.Context, *sql.Tx) error { return nil }); err != nil {
				t.Errorf("queued operation: %v", err)
			}
		})
	}
	deadline := time.Now().Add(time.Second)
	for store.batch.depth() < sqliteQueueCapacity && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.batch.depth() != sqliteQueueCapacity {
		close(release)
		workers.Wait()
		_ = store.close()
		t.Fatal("queue did not fill")
	}
	if err := store.transaction(func(context.Context, *sql.Tx) error { return nil }); !errors.Is(err, errStorageFailure) {
		t.Fatal("full queue did not reject")
	}
	if store.batch.rejected.Load() != 1 {
		t.Fatal("queue rejection was not counted")
	}
	closed := make(chan struct{})
	go func() {
		_ = store.close()
		close(closed)
	}()
	close(release)
	workers.Wait()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not drain queued operations")
	}
	if err := store.transaction(func(context.Context, *sql.Tx) error { return nil }); !errors.Is(err, errStorageFailure) {
		t.Fatal("closed queue accepted an operation")
	}
}
