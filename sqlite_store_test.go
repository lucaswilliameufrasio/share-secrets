package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func migratedSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.db")
	command := exec.Command("go", "tool", "goose", "-env", "none", "-dir", "migrations/sqlite", "sqlite3", path, "up")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Goose migration failed: %v: %s", err, output)
	}
	return path
}

func TestSQLiteLifecyclePersistsAcrossConnections(t *testing.T) {
	path := migratedSQLite(t)
	a, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.close() })
	b, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.close() })
	p := validPayload()
	id, _, tracking, err := a.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	var claimToken string
	for _, store := range []*sqliteStore{a, b} {
		wg.Go(func() {
			_, token, err := store.claim(id)
			if err == nil {
				mu.Lock()
				winners++
				claimToken = token
				mu.Unlock()
			} else if !errors.Is(err, errSecretClaimed) {
				t.Errorf("unexpected claim failure: %v", err)
			}
		})
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("claims won: %d", winners)
	}
	if err := b.acknowledge(id, claimToken, "invalid"); !errors.Is(err, errInvalidProof) {
		t.Fatalf("bad proof: %v", err)
	}
	if err := b.acknowledge(id, claimToken, validProof()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.claim(id); !errors.Is(err, errSecretUnavailable) {
		t.Fatalf("consumed link claim: %v", err)
	}
	status, err := a.status(id, tracking)
	if err != nil || status.Status != "used" {
		t.Fatalf("tracking after consume: %+v %v", status, err)
	}
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM payloads WHERE id=?", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("payload remains: %d %v", count, err)
	}
	metrics, err := a.readMetrics()
	if err != nil || metrics.ConsumedTotal != 1 || metrics.CleanupPending != 1 {
		t.Fatalf("metrics/purge generation: %+v %v", metrics, err)
	}
	if err := b.checkpoint(); err != nil {
		t.Fatal(err)
	}
	metrics, err = a.readMetrics()
	if err != nil || metrics.CleanupPending != 0 {
		t.Fatalf("checkpoint not acknowledged: %+v %v", metrics, err)
	}
	if err := b.close(); err != nil {
		t.Fatal(err)
	}
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.close() }()
	status, err = reopened.status(id, tracking)
	if err != nil || status.Status != "used" {
		t.Fatalf("restart: %+v %v", status, err)
	}
	for _, file := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(p.Ciphertext)) {
			t.Fatal("synthetic ciphertext remains after secure delete and checkpoint")
		}
	}
}

func TestSQLiteRejectsMissingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := openSQLiteStore(path, time.Hour, 500); err == nil {
		t.Fatal("unmigrated database accepted")
	}
}

func TestSQLiteMetricsRouteRequiresTokenAndReportsPending(t *testing.T) {
	store, err := openSQLiteStore(migratedSQLite(t), time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.close() }()
	p := validPayload()
	id, _, _, err := store.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.claim(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.acknowledge(id, token, validProof()); err != nil {
		t.Fatal(err)
	}
	metricsToken := "synthetic-metrics-token-for-tests-only"
	handler := (&server{store: store, metricsEnabled: true, metricsTokenHash: sha256.Sum256([]byte(metricsToken))}).routes()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized metrics status %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+metricsToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"cleanupPending":1`)) {
		t.Fatalf("authorized metrics status=%d, cleanup flag missing", response.Code)
	}
}

func TestSQLiteExpiryAndCapacityAcrossConnections(t *testing.T) {
	path := migratedSQLite(t)
	a, err := openSQLiteStore(path, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.close() }()
	b, err := openSQLiteStore(path, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.close() }()
	now := time.Now()
	a.now = func() time.Time { return now }
	b.now = func() time.Time { return now }
	p := validPayload()
	id, _, tracking, err := a.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Minute); !errors.Is(err, errSecretStoreFull) {
		t.Fatalf("capacity across connections: %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := b.claim(id); !errors.Is(err, errSecretUnavailable) {
		t.Fatalf("expired claim: %v", err)
	}
	status, err := a.status(id, tracking)
	if err != nil || status.Status != "expired" {
		t.Fatalf("expired status: %+v %v", status, err)
	}
	if _, _, _, err := b.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Minute); err != nil {
		t.Fatalf("expired capacity not released: %v", err)
	}
}

func TestSQLiteBusyCheckpointKeepsPurgePending(t *testing.T) {
	path := migratedSQLite(t)
	a, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.close() }()
	b, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.close() }()
	p := validPayload()
	id, _, _, err := a.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := b.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow("SELECT count(*) FROM payloads").Scan(&count); err != nil {
		t.Fatal(err)
	}
	// Release the immediate writer lock: use a dedicated read-only connection
	// below instead of holding an IMMEDIATE transaction on the app connection.
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	readDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readDB.Close() }()
	reader, err = readDB.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow("SELECT count(*) FROM payloads").Scan(&count); err != nil {
		t.Fatal(err)
	}
	_, claim, err := a.claim(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.acknowledge(id, claim, validProof()); err != nil {
		t.Fatal(err)
	}
	if err := a.checkpoint(); err == nil {
		t.Fatal("checkpoint unexpectedly succeeded with old reader")
	}
	metrics, err := a.readMetrics()
	if err != nil || metrics.CleanupPending != 1 {
		t.Fatalf("pending purge lost: %+v %v", metrics, err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := b.checkpoint(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteBusyWriterFailsClosedAndRecovers(t *testing.T) {
	path := migratedSQLite(t)
	a, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.close() }()
	b, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.close() }()
	p := validPayload()
	id, _, tracking, err := a.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := a.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&server{store: b}).routes()
	request := httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/claim", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("locked database must return 503, got %d", response.Code)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	status, err := b.status(id, tracking)
	if err != nil || status.Status != "active" {
		t.Fatalf("lock failure changed lifecycle: %+v %v", status, err)
	}
	if _, _, err := b.claim(id); err != nil {
		t.Fatalf("claim did not recover: %v", err)
	}
}

func TestSQLiteRollbackDoesNotConsumeAfterProcessDeath(t *testing.T) {
	if os.Getenv("SQLITE_CRASH_CHILD") == "1" {
		store, err := openSQLiteStore(os.Getenv("SQLITE_CRASH_PATH"), time.Hour, 500)
		if err != nil {
			os.Exit(4)
		}
		tx, err := store.db.BeginTx(context.Background(), nil)
		if err != nil {
			os.Exit(5)
		}
		if _, err := tx.Exec("DELETE FROM payloads"); err != nil {
			os.Exit(6)
		}
		os.Exit(0) // Simulate abrupt process termination before commit.
	}
	path := migratedSQLite(t)
	store, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	p := validPayload()
	id, _, _, err := store.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSQLiteRollbackDoesNotConsumeAfterProcessDeath$")
	child.Env = []string{"SQLITE_CRASH_CHILD=1", "SQLITE_CRASH_PATH=" + path}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash simulation failed: %v (%d diagnostic bytes)", err, len(output))
	}
	reopened, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.close() }()
	item, _, err := reopened.claim(id)
	if err != nil || item.Ciphertext != p.Ciphertext {
		t.Fatalf("uncommitted deletion did not roll back: %v", err)
	}
}

func TestSQLiteTwoApplicationProcessesShareOneTimeLifecycle(t *testing.T) {
	path := migratedSQLite(t)
	binary := filepath.Join(t.TempDir(), "application")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("application build failed: %v (%d diagnostic bytes)", err, len(output))
	}
	start := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		process := exec.Command(binary)
		process.Env = []string{"ADDR=" + address, "STORAGE_BACKEND=sqlite", "SQLITE_PATH=" + path, "SQLITE_NO_PAYLOAD_BACKUP_ACK=yes"}
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = process.Process.Kill()
			_ = process.Wait()
		})
		client := &http.Client{Timeout: 200 * time.Millisecond}
		for i := 0; i < 100; i++ {
			response, err := client.Get("http://" + address + "/healthz")
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusNoContent {
					return "http://" + address
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("SQLite application did not become ready")
		return ""
	}
	blue, green := start(), start()
	body, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(blue+"/api/secrets", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created createResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		_ = response.Body.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status: %d", response.StatusCode)
	}
	type claimResult struct {
		status int
		token  string
		err    error
	}
	results := make(chan claimResult, 2)
	for _, origin := range []string{blue, green} {
		go func() {
			response, err := http.Post(origin+"/api/secrets/"+created.ID+"/claim", "application/json", nil)
			if err != nil {
				results <- claimResult{err: err}
				return
			}
			defer func() { _ = response.Body.Close() }()
			var payload claimResponse
			if response.StatusCode == http.StatusOK {
				err = json.NewDecoder(response.Body).Decode(&payload)
			}
			results <- claimResult{status: response.StatusCode, token: payload.Token, err: err}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || ((first.status != 200 || second.status != 409) && (first.status != 409 || second.status != 200)) {
		t.Fatalf("two-process claims: statuses=%d,%d errors=%v,%v", first.status, second.status, first.err, second.err)
	}
	claim := first.token
	if claim == "" {
		claim = second.token
	}
	ack, err := json.Marshal(claimRequest{Token: claim, Proof: validProof()})
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(green+"/api/secrets/"+created.ID+"/ack", "application/json", bytes.NewReader(ack))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("cross-slot ACK status: %d", response.StatusCode)
	}
	response, err = http.Post(blue+"/api/secrets/"+created.ID+"/claim", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-slot consumed claim status: %d", response.StatusCode)
	}
}

func TestSQLiteProcessSIGTERMDrainsInFlightWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics differ on Windows")
	}
	path := migratedSQLite(t)
	binary := filepath.Join(t.TempDir(), "application")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("application build failed: %v (%d diagnostic bytes)", err, len(output))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary)
	process.Env = []string{"ADDR=" + address, "STORAGE_BACKEND=sqlite", "SQLITE_PATH=" + path, "SQLITE_NO_PAYLOAD_BACKUP_ACK=yes"}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- process.Wait() }()
	t.Cleanup(func() { _ = process.Process.Kill() })
	client := &http.Client{Timeout: 5 * time.Second}
	ready := false
	for i := 0; i < 100; i++ {
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("application did not become ready")
	}
	locker, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = locker.close() }()
	lock, err := locker.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback() }()
	body, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan struct{}, 1)
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote <- struct{}{} }}
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodPost, "http://"+address+"/api/secrets", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	result := make(chan int, 1)
	go func() {
		response, err := client.Do(request)
		if err != nil {
			result <- 0
			return
		}
		_ = response.Body.Close()
		result <- response.StatusCode
	}()
	<-wrote
	time.Sleep(100 * time.Millisecond)
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if status := <-result; status != http.StatusCreated {
		t.Fatalf("admitted write failed during drain: %d", status)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("process did not shut down cleanly: %v", err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("SIGTERM drain exceeded shutdown bound")
	}
	metrics, err := locker.readMetrics()
	if err != nil || metrics.CreatedTotal != 1 || metrics.Active != 1 {
		t.Fatalf("acknowledged write was not durable: %+v %v", metrics, err)
	}
}

func BenchmarkSQLiteLifecycle(b *testing.B) {
	for _, size := range []int{16, 1024, 16 * 1024, 44 * 1024} {
		b.Run(fmt.Sprintf("payload_%d", size), func(b *testing.B) {
			benchmarkSQLiteLifecycleSize(b, size)
		})
	}
}

func benchmarkSQLiteLifecycleSize(b *testing.B, size int) {
	path := filepath.Join(b.TempDir(), "secrets.db")
	command := exec.Command("go", "tool", "goose", "-env", "none", "-dir", "migrations/sqlite", "sqlite3", path, "up")
	if output, err := command.CombinedOutput(); err != nil {
		b.Fatalf("migration failed: %v (%d diagnostic bytes)", err, len(output))
	}
	store, err := openSQLiteStore(path, time.Hour, 500)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = store.close() }()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				store.cleanupExpired()
			}
		}
	}()
	defer func() {
		close(done)
		<-stopped
	}()
	p := validPayload()
	p.Ciphertext = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", size)))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id, _, tracking, err := store.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
			if err != nil {
				b.Error(err)
				return
			}
			if _, err := store.status(id, tracking); err != nil {
				b.Error(err)
				return
			}
			_, token, err := store.claim(id)
			if err != nil {
				b.Error(err)
				return
			}
			if err := store.acknowledge(id, token, validProof()); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
