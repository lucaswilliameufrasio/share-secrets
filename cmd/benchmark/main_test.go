package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFixtureDecryptsAndFitsWireLimit(t *testing.T) {
	for _, size := range []int{1024, 16 * 1024, 44 * 1024} {
		f, err := makeFixture(size)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.body) >= 64*1024 {
			t.Fatalf("fixture exceeds wire limit: %d", len(f.body))
		}
		iv, err := base64.RawURLEncoding.DecodeString(f.iv)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := base64.RawURLEncoding.DecodeString(f.ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := f.gcm.Open(nil, iv, sealed, nil)
		if err != nil || !bytes.Equal(plain, f.plaintext) {
			t.Fatal("fixture failed authenticated round trip")
		}
		sealed[0] ^= 1
		if _, err := f.gcm.Open(nil, iv, sealed, nil); err == nil {
			t.Fatal("tampered ciphertext accepted")
		}
	}
}

func TestValidationRejectsRemoteTargetsAndUnboundedWork(t *testing.T) {
	valid := options{Target: "http://127.0.0.1:4173", Scenario: "mixed", Duration: time.Second, Rate: 10, Workers: 4}
	if err := validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"https://127.0.0.1:4173", "http://example.com:80", "http://localhost:80", "http://user@127.0.0.1:80", "http://127.0.0.1:80/path", "http://127.0.0.1:80?q=x"} {
		invalid := valid
		invalid.Target = target
		if err := validate(invalid); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
	invalid := valid
	invalid.Rate = 1_000_000
	if err := validate(invalid); err == nil {
		t.Fatal("unbounded rate accepted")
	}
}

func TestLatencyPercentiles(t *testing.T) {
	result := summarize([]time.Duration{5 * time.Millisecond, time.Millisecond, 3 * time.Millisecond})
	if result.Count != 3 || result.P50 != 3 || result.Max != 5 {
		t.Fatalf("incorrect latency summary: %+v", result)
	}
	if summarize(nil).Count != 0 {
		t.Fatal("empty samples have a count")
	}
	if summarize([]time.Duration{time.Millisecond, 100 * time.Millisecond}).P99 != 100 {
		t.Fatal("nearest-rank p99 must include the maximum for small samples")
	}
}

func TestCancelledArrivalWindowIsReported(t *testing.T) {
	d, err := newDriver(options{Target: "http://127.0.0.1:4173", Scenario: "mixed", Duration: time.Second, Rate: 10, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	d.startedAt = time.Now().Add(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.arrivals(ctx)
	if d.result.Offered != 0 || d.result.Failures["arrival deadline"] != 1 {
		t.Fatal("cancelled arrival window was not reported as incomplete")
	}
}

func TestJourneyAgainstRealApplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real test server: %v (%d diagnostic bytes)", err, len(output))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	process := exec.CommandContext(ctx, binary)
	process.Env = []string{"ADDR=" + address, "METRICS_TOKEN=" + token}
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	target := "http://" + address
	client := &http.Client{Timeout: 100 * time.Millisecond}
	ready := false
	for i := 0; i < 100; i++ {
		response, err := client.Get(target + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 204 {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("real application did not become ready on owned port")
	}
	d, err := newDriver(options{Target: target, Scenario: "mixed", Duration: time.Second, Rate: 3, Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	d.metricsToken = token
	result := d.run(ctx)
	if len(result.Failures) != 0 || result.Completed != 3 || result.Dropped != 0 {
		t.Fatalf("real HTTP journeys failed: completed=%d dropped=%d failures=%v", result.Completed, result.Dropped, result.Failures)
	}
	if result.Statuses["invalid_ack:409"] != 1 || result.Statuses["release:204"] != 1 || result.Statuses["second_claim:404"] != 3 {
		t.Fatal("negative proof/release/single-use contracts not exercised")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(token)) || bytes.Contains(encoded, []byte(d.fixtures[0].proof)) || bytes.Contains(encoded, []byte(target)) {
		t.Fatal("report leaks token, proof or target URL")
	}
}
