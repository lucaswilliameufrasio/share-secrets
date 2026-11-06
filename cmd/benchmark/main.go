// benchmark drives synthetic HTTP journeys against an explicitly local server.
// It never writes response bodies, IDs, tokens or ciphertext to its report.
package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type fixture struct {
	body       []byte
	ciphertext string
	iv         string
	proof      string
	plaintext  []byte
	gcm        cipher.AEAD
}

type options struct {
	Target   string        `json:"-"`
	Scenario string        `json:"scenario"`
	Duration time.Duration `json:"durationNs"`
	Rate     int           `json:"offeredCyclesPerSecond"`
	Workers  int           `json:"maxInFlightCycles"`
	Seed     int           `json:"seedActiveLinks"`
	Terminal int           `json:"seedTerminalLinks"`
}

type latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50Ms"`
	P95   float64 `json:"p95Ms"`
	P99   float64 `json:"p99Ms"`
	Max   float64 `json:"maxMs"`
}

type report struct {
	Schema            int                    `json:"schemaVersion"`
	Options           options                `json:"options"`
	Seconds           float64                `json:"seconds"`
	WindowStartUnixNs int64                  `json:"windowStartUnixNs"`
	WindowEndUnixNs   int64                  `json:"windowEndUnixNs"`
	Offered           int                    `json:"offered"`
	Started           int                    `json:"started"`
	Dropped           int                    `json:"dropped"`
	Completed         int                    `json:"completed"`
	Rejected          int                    `json:"rejected"`
	Failures          map[string]int         `json:"failures"`
	Statuses          map[string]int         `json:"httpStatuses"`
	Endpoints         map[string]latency     `json:"endpointLatency"`
	Cycles            latency                `json:"cycleLatency"`
	Scheduling        latency                `json:"schedulingLag"`
	MetricsBefore     map[string]int64       `json:"metricsBefore"`
	MetricsAfter      map[string]int64       `json:"metricsAfter"`
	PerSecond         map[int]map[string]int `json:"perSecond"`
	DiagnosticHealthy bool                   `json:"diagnosticHealthy"`
	RecoveryPassed    bool                   `json:"recoveryPassed"`
	RecoveryMs        float64                `json:"recoveryMs"`
}

type driver struct {
	client       *http.Client
	target       string
	metricsToken string
	fixtures     []fixture
	mu           sync.Mutex
	result       report
	endpoint     map[string][]time.Duration
	cycles       []time.Duration
	lag          []time.Duration
	startedAt    time.Time
}

func makeFixture(size int) (fixture, error) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	proof := make([]byte, 32)
	for _, value := range [][]byte{key, proof} {
		if _, err := rand.Read(value); err != nil {
			return fixture{}, err
		}
	}
	if _, err := rand.Read(nonce); err != nil {
		return fixture{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fixture{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fixture{}, err
	}
	encodedProof := base64.RawURLEncoding.EncodeToString(proof)
	plaintext, err := json.Marshal(map[string]string{"secret": strings.Repeat("x", size), "proof": encodedProof})
	if err != nil {
		return fixture{}, err
	}
	sealed := base64.RawURLEncoding.EncodeToString(gcm.Seal(nil, nonce, plaintext, nil))
	iv := base64.RawURLEncoding.EncodeToString(nonce)
	digest := sha256.Sum256(proof)
	body, err := json.Marshal(map[string]any{
		"ciphertext": sealed, "iv": iv,
		"proofHash": base64.RawURLEncoding.EncodeToString(digest[:]), "ttlSeconds": 60,
	})
	return fixture{body: body, ciphertext: sealed, iv: iv, proof: encodedProof, plaintext: plaintext, gcm: gcm}, err
}

func validate(o options) error {
	u, err := url.Parse(o.Target)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("target must be http://127.0.0.1:PORT without path, credentials or query")
	}
	if o.Duration < time.Second || o.Duration > 10*time.Minute || o.Rate < 1 || o.Rate > 2000 || o.Workers < 1 || o.Workers > 512 || o.Seed < 0 || o.Seed > 500 {
		return errors.New("duration/rate/workers/seed outside safe local bounds")
	}
	if o.Terminal < 0 || o.Terminal > 9500 || int64(o.Duration/time.Second)*int64(o.Rate) > 200_000 {
		return errors.New("seed/sample budget exceeded")
	}
	switch o.Scenario {
	case "mixed", "burst", "policy", "capacity", "expiry", "concurrency":
		return nil
	default:
		return errors.New("unknown scenario")
	}
}

func newDriver(o options) (*driver, error) {
	if err := validate(o); err != nil {
		return nil, err
	}
	d := &driver{
		target: o.Target, metricsToken: os.Getenv("BENCH_METRICS_TOKEN"),
		client: &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{
			MaxIdleConns: 1024, MaxIdleConnsPerHost: 512, MaxConnsPerHost: 1024,
			IdleConnTimeout: 10 * time.Second, ResponseHeaderTimeout: 3 * time.Second,
		}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		endpoint: make(map[string][]time.Duration),
		result: report{Schema: 1, Options: o, Failures: make(map[string]int), Statuses: make(map[string]int),
			Endpoints: make(map[string]latency), PerSecond: make(map[int]map[string]int)},
	}
	for _, size := range []int{1024, 16 * 1024, 44 * 1024} {
		f, err := makeFixture(size)
		if err != nil {
			return nil, err
		}
		d.fixtures = append(d.fixtures, f)
	}
	return d, nil
}

func (d *driver) request(ctx context.Context, method, endpoint, path, token string, body []byte, output any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, d.target+path, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("request construction")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	start := time.Now()
	response, err := d.client.Do(request)
	if err != nil {
		return 0, errors.New("transport")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024))
	d.mu.Lock()
	d.result.Statuses[fmt.Sprintf("%s:%d", endpoint, response.StatusCode)]++
	d.endpoint[endpoint] = append(d.endpoint[endpoint], time.Since(start))
	d.mu.Unlock()
	if err != nil {
		return response.StatusCode, errors.New("body read")
	}
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.Unmarshal(data, output); err != nil {
			return response.StatusCode, errors.New("response decode")
		}
	}
	return response.StatusCode, nil
}

type created struct {
	ID    string `json:"id"`
	Token string `json:"trackingToken"`
}

type claimed struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
	Token      string `json:"token"`
}

func (d *driver) create(ctx context.Context, f fixture) (created, int, error) {
	var result created
	code, err := d.request(ctx, "POST", "create", "/api/secrets", "", f.body, &result)
	if err == nil && code == 201 && (len(result.ID) != 32 || len(result.Token) != 43) {
		err = errors.New("creation contract")
	}
	return result, code, err
}

func (d *driver) expect(ctx context.Context, method, endpoint, path, token string, body []byte, expected int) error {
	code, err := d.request(ctx, method, endpoint, path, token, body, nil)
	if err != nil || code != expected {
		return fmt.Errorf("%s contract", endpoint)
	}
	return nil
}

func (d *driver) status(ctx context.Context, c created, expected string) error {
	var result struct {
		Status string `json:"status"`
	}
	code, err := d.request(ctx, "GET", "status", "/api/secrets/"+c.ID+"/status", c.Token, nil, &result)
	if err != nil || code != 200 || result.Status != expected {
		return errors.New("status contract")
	}
	return nil
}

func (d *driver) journey(ctx context.Context, sequence int) (bool, error) {
	f := d.fixtures[0]
	if sequence%100 == 99 {
		f = d.fixtures[2]
	} else if sequence%10 == 9 {
		f = d.fixtures[1]
	}
	c, code, err := d.create(ctx, f)
	if err != nil {
		return false, err
	}
	if code == 429 || code == 503 {
		return true, nil
	}
	if code != 201 {
		return false, errors.New("create contract")
	}
	base := "/api/secrets/" + c.ID
	if err := d.status(ctx, c, "active"); err != nil {
		return false, err
	}
	var claim claimed
	code, err = d.request(ctx, "POST", "claim", base+"/claim", "", nil, &claim)
	if err != nil || code != 200 || len(claim.Token) != 32 || claim.Ciphertext != f.ciphertext || claim.IV != f.iv {
		return false, errors.New("claim contract")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(claim.IV)
	if err != nil {
		return false, errors.New("IV decode")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(claim.Ciphertext)
	if err != nil {
		return false, errors.New("ciphertext decode")
	}
	plaintext, err := f.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil || !bytes.Equal(plaintext, f.plaintext) {
		return false, errors.New("decryption contract")
	}
	if sequence%10 == 0 {
		badProof, _ := json.Marshal(map[string]string{"token": claim.Token, "proof": "invalid"})
		if err := d.expect(ctx, "POST", "invalid_ack", base+"/ack", "", badProof, 409); err != nil {
			return false, err
		}
		release, _ := json.Marshal(map[string]string{"token": claim.Token})
		if err := d.expect(ctx, "POST", "release", base+"/release", "", release, 204); err != nil {
			return false, err
		}
		code, err = d.request(ctx, "POST", "reclaim", base+"/claim", "", nil, &claim)
		if err != nil || code != 200 {
			return false, errors.New("reclaim contract")
		}
	}
	ack, _ := json.Marshal(map[string]string{"token": claim.Token, "proof": f.proof})
	if err := d.expect(ctx, "POST", "ack", base+"/ack", "", ack, 204); err != nil {
		return false, err
	}
	if err := d.status(ctx, c, "used"); err != nil {
		return false, err
	}
	return false, d.expect(ctx, "POST", "second_claim", base+"/claim", "", nil, 404)
}

func (d *driver) metrics(ctx context.Context) map[string]int64 {
	var values map[string]int64
	code, err := d.request(ctx, "GET", "metrics", "/api/metrics", d.metricsToken, nil, &values)
	if err != nil || code != 200 {
		return nil
	}
	return values
}

func summarize(values []time.Duration) latency {
	if len(values) == 0 {
		return latency{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	percentile := func(p float64) float64 {
		index := max(0, int(math.Ceil(float64(len(values))*p))-1)
		return float64(values[index]) / float64(time.Millisecond)
	}
	return latency{Count: len(values), P50: percentile(.50), P95: percentile(.95), P99: percentile(.99), Max: percentile(1)}
}

func (d *driver) record(start time.Time, rejected bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cycles = append(d.cycles, time.Since(start))
	second := int(start.Sub(d.startedAt).Seconds())
	if d.result.PerSecond[second] == nil {
		d.result.PerSecond[second] = make(map[string]int)
	}
	switch {
	case err != nil:
		d.result.Failures[err.Error()]++
		d.result.PerSecond[second]["failed"]++
	case rejected:
		d.result.Rejected++
		d.result.PerSecond[second]["rejected"]++
	default:
		d.result.Completed++
		d.result.PerSecond[second]["completed"]++
	}
}

// Fixed arrivals, no unbounded queue and no catch-up burst. Late arrivals and
// exhausted worker slots are counted as dropped rather than hidden by slowdown.
func (d *driver) arrivals(ctx context.Context) {
	o := d.result.Options
	slots := make(chan struct{}, o.Workers)
	var workers sync.WaitGroup
	interval := time.Second / time.Duration(o.Rate)
	count := int(o.Duration / interval)
	for i := 0; i < count; i++ {
		planned := d.startedAt.Add(time.Duration(i) * interval)
		if wait := time.Until(planned); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				workers.Wait()
				d.result.Failures["arrival deadline"]++
				return
			case <-timer.C:
			}
		}
		d.result.Offered++
		lag := time.Since(planned)
		d.lag = append(d.lag, lag)
		if lag > 2*interval {
			d.result.Dropped++
			continue
		}
		select {
		case slots <- struct{}{}:
			d.result.Started++
			sequence := i
			workers.Go(func() {
				defer func() { <-slots }()
				start := time.Now()
				rejected, err := d.journey(ctx, sequence)
				d.record(start, rejected, err)
			})
		default:
			d.result.Dropped++
		}
	}
	workers.Wait()
}

func (d *driver) special(ctx context.Context) error {
	o := d.result.Options
	if o.Scenario == "expiry" {
		c, code, err := d.create(ctx, d.fixtures[0])
		if err != nil || code != 201 {
			return errors.New("expiry create")
		}
		if err := d.status(ctx, c, "active"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("expiry deadline")
		case <-time.After(61 * time.Second):
		}
		if err := d.status(ctx, c, "expired"); err != nil {
			return err
		}
		return d.expect(ctx, "POST", "expired_claim", "/api/secrets/"+c.ID+"/claim", "", nil, 404)
	}
	if o.Scenario == "capacity" {
		for i := 0; i < 20; i++ {
			_, code, err := d.create(ctx, d.fixtures[0])
			if err != nil || code != 503 {
				return errors.New("capacity rejection")
			}
		}
		return nil
	}
	if o.Scenario == "concurrency" {
		return d.concurrency(ctx)
	}
	return errors.New("invalid special scenario")
}

func (d *driver) concurrency(ctx context.Context) error {
	u, _ := url.Parse(d.target)
	var connections []net.Conn
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	// Hold handlers in JSON decoding, below the production 10s read timeout.
	for i := 0; i < 270; i++ {
		connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return errors.New("concurrency connection")
		}
		connections = append(connections, connection)
		if _, err := fmt.Fprintf(connection, "POST /api/secrets HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"); err != nil {
			return errors.New("concurrency header")
		}
	}
	defer d.client.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	rejected := false
	for time.Now().Before(deadline) {
		code, err := d.request(ctx, "GET", "concurrency_probe", "/api/config", "", nil, nil)
		if err == nil && code == 503 {
			rejected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !rejected {
		return errors.New("concurrency bound not observed")
	}
	for _, connection := range connections {
		_ = connection.Close()
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := d.expect(ctx, "GET", "recovery", "/api/config", "", nil, 200); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("concurrency recovery")
}

func (d *driver) run(ctx context.Context) report {
	// Seeding and the measured window have independent budgets. A slow disk
	// must not consume the window's time or silently reduce offered arrivals.
	setupBudget := max(5*time.Minute, time.Duration(d.result.Options.Terminal)*300*time.Millisecond+2*time.Minute)
	setupCtx, stopSetup := context.WithTimeout(ctx, setupBudget)
	// Real HTTP journeys populate retained metadata before the measured window.
	for i := 0; i < d.result.Options.Terminal; i++ {
		rejected, err := d.journey(setupCtx, i)
		if err != nil || rejected {
			stopSetup()
			d.result.Failures["terminal seed failed"]++
			return d.result
		}
	}
	for i := 0; i < d.result.Options.Seed; i++ {
		_, code, err := d.create(setupCtx, d.fixtures[0])
		if err != nil || code != 201 {
			stopSetup()
			d.result.Failures["seed failed"]++
			return d.result
		}
	}
	d.result.MetricsBefore = d.metrics(setupCtx)
	stopSetup()
	windowCtx, stopWindow := context.WithTimeout(ctx, d.result.Options.Duration+75*time.Second)
	defer stopWindow()
	d.endpoint = make(map[string][]time.Duration)
	d.result.Statuses = make(map[string]int)
	d.startedAt = time.Now()
	d.result.WindowStartUnixNs = d.startedAt.UnixNano()
	switch d.result.Options.Scenario {
	case "mixed", "burst", "policy":
		d.arrivals(windowCtx)
	default:
		d.result.Offered = 1
		d.result.Started = 1
		d.record(d.startedAt, false, d.special(windowCtx))
	}
	d.result.Seconds = time.Since(d.startedAt).Seconds()
	d.result.WindowEndUnixNs = time.Now().UnixNano()
	if d.result.Options.Scenario == "burst" {
		start := time.Now()
		rejected, err := d.journey(windowCtx, 1001)
		d.result.RecoveryPassed = err == nil && !rejected
		d.result.RecoveryMs = float64(time.Since(start)) / float64(time.Millisecond)
		if !d.result.RecoveryPassed {
			d.result.Failures["burst recovery"]++
		}
	}
	d.result.MetricsAfter = d.metrics(windowCtx)
	d.client.CloseIdleConnections()
	if d.result.MetricsBefore == nil || d.result.MetricsAfter == nil {
		d.result.Failures["metrics unavailable"]++
	}
	for name, values := range d.endpoint {
		d.result.Endpoints[name] = summarize(values)
	}
	d.result.Cycles = summarize(d.cycles)
	d.result.Scheduling = summarize(d.lag)
	d.result.DiagnosticHealthy = len(d.result.Failures) == 0 && d.result.Offered > 0 &&
		float64(d.result.Completed)/float64(d.result.Offered) >= .99 && d.result.Cycles.P99 < 1000
	return d.result
}

func main() {
	var o options
	flag.StringVar(&o.Target, "target", "", "owned local HTTP server URL")
	flag.StringVar(&o.Scenario, "scenario", "mixed", "mixed|burst|policy|capacity|expiry|concurrency")
	flag.DurationVar(&o.Duration, "duration", 10*time.Second, "bounded arrival window")
	flag.IntVar(&o.Rate, "rate", 20, "offered journeys per second")
	flag.IntVar(&o.Workers, "workers", 128, "maximum concurrent journeys; no waiting queue")
	flag.IntVar(&o.Seed, "seed", 0, "active links to create before the window")
	flag.IntVar(&o.Terminal, "terminal", 0, "terminal links to create via HTTP before the window")
	flag.Parse()
	d, err := newDriver(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	result := d.run(context.Background())
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "report write failed")
		os.Exit(1)
	}
	if len(result.Failures) != 0 {
		os.Exit(2)
	}
}
