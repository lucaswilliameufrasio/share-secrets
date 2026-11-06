package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func validPayload() createRequest {
	proof := validProof()
	proofBytes, _ := base64.RawURLEncoding.DecodeString(proof)
	proofHash := sha256.Sum256(proofBytes)
	return createRequest{
		Ciphertext: base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
		IV:         base64.RawURLEncoding.EncodeToString([]byte("0123456789ab")),
		ProofHash:  base64.RawURLEncoding.EncodeToString(proofHash[:]),
	}
}

func validProof() string {
	return base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func TestSecretIsConsumedOnlyAfterAcknowledgement(t *testing.T) {
	store := newSecretStore(time.Hour)
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	_, token, err := store.claim(id)
	if err != nil {
		t.Fatalf("claim secret: %v", err)
	}
	if err := store.acknowledge(id, token, validProof()); err != nil {
		t.Fatalf("acknowledge secret: %v", err)
	}
	if _, _, err := store.claim(id); !errors.Is(err, errSecretUnavailable) {
		t.Fatalf("second claim should fail as consumed, got %v", err)
	}
}

func TestFailedDecryptionCanReleaseAndRetryClaim(t *testing.T) {
	store := newSecretStore(time.Hour)
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatal(err)
	}
	first, token, err := store.claim(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.release(id, token); err != nil {
		t.Fatal(err)
	}
	second, retryToken, err := store.claim(id)
	if err != nil {
		t.Fatalf("retry after failed decrypt: %v", err)
	}
	if first.Ciphertext != second.Ciphertext || first.IV != second.IV {
		t.Fatal("retry returned different encrypted data")
	}
	if err := store.acknowledge(id, retryToken, validProof()); err != nil {
		t.Fatal(err)
	}
}

func TestClaimLeaseExpiresAndCanBeRetried(t *testing.T) {
	store := newSecretStore(time.Hour)
	now := time.Now()
	store.now = func() time.Time { return now }
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.claim(id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.claim(id); !errors.Is(err, errSecretClaimed) {
		t.Fatalf("expected active claim, got %v", err)
	}
	now = now.Add(claimLease)
	if _, token, err := store.claim(id); err != nil {
		t.Fatalf("claim after lease timeout: %v", err)
	} else if err := store.acknowledge(id, token, validProof()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClaimsHaveSingleWinner(t *testing.T) {
	store := newSecretStore(time.Hour)
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { _, _, err := store.claim(id); results <- err }()
	}
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("expected one claim winner, got %v and %v", a, b)
	}
}

func TestExpiredSecretCannotBeConsumed(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := store.claim(id); !errors.Is(err, errSecretUnavailable) {
		t.Fatalf("expired secret should be unavailable, got %v", err)
	}
}

func TestTrackingStatusRequiresScopedTokenAndReflectsUse(t *testing.T) {
	store := newSecretStore(time.Hour)
	payload := validPayload()
	id, _, trackingToken, err := store.createWithTTL(payload.Ciphertext, payload.IV, payload.ProofHash, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.status(id, "wrong-token"); !errors.Is(err, errSecretUnavailable) {
		t.Fatalf("invalid tracking token status error=%v, want unavailable", err)
	}
	active, err := store.status(id, trackingToken)
	if err != nil || active.Status != "active" {
		t.Fatalf("active tracking status=%+v, err=%v", active, err)
	}
	_, claimToken, err := store.claim(id)
	if err != nil {
		t.Fatal(err)
	}
	opening, err := store.status(id, trackingToken)
	if err != nil || opening.Status != "opening" || opening.ClaimCount != 1 {
		t.Fatalf("opening tracking status=%+v, err=%v", opening, err)
	}
	if err := store.acknowledge(id, claimToken, validProof()); err != nil {
		t.Fatal(err)
	}
	used, err := store.status(id, trackingToken)
	if err != nil || used.Status != "used" || used.UsedAt == nil {
		t.Fatalf("used tracking status=%+v, err=%v", used, err)
	}
	stored := store.secrets[id]
	if stored.Ciphertext != "" || stored.IV != "" || stored.ProofHash != ([sha256.Size]byte{}) {
		t.Fatal("consumed encrypted payload was not cleared from memory")
	}
}

func TestExpiredStatusClearsEncryptedPayloadAndRemainsTrackable(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	payload := validPayload()
	id, _, trackingToken, err := store.createWithTTL(payload.Ciphertext, payload.IV, payload.ProofHash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	status, err := store.status(id, trackingToken)
	if err != nil || status.Status != "expired" {
		t.Fatalf("expired tracking status=%+v, err=%v", status, err)
	}
	if store.activeCount != 0 || store.secrets[id].Ciphertext != "" {
		t.Fatal("expired secret retained active capacity or ciphertext")
	}
	if got := store.metrics().ExpiredTotal; got != 1 {
		t.Fatalf("expired total=%d, want 1", got)
	}
}

func TestClaimHTTPFlowRequiresValidAckAndSupportsRelease(t *testing.T) {
	store := newSecretStore(time.Hour)
	p := validPayload()
	id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&server{store: store}).routes()
	get := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/claim", nil))
		return response
	}
	first := get()
	if first.Code != http.StatusOK {
		t.Fatalf("claim response: %d", first.Code)
	}
	var payload claimResponse
	if err := json.Unmarshal(first.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token == "" {
		t.Fatal("claim response omitted token")
	}
	if strings.Contains(first.Body.String(), "proofHash") || strings.Contains(first.Body.String(), p.ProofHash) {
		t.Fatal("claim response exposed the proof verifier")
	}
	if got := get().Code; got != http.StatusConflict {
		t.Fatalf("concurrent claim status=%d, want 409", got)
	}

	badAck := httptest.NewRecorder()
	handler.ServeHTTP(badAck, newJSONRequest(http.MethodPost, "/api/secrets/"+id+"/ack", `{"token":"wrong"}`))
	if badAck.Code != http.StatusConflict {
		t.Fatalf("bad ack status=%d, want 409", badAck.Code)
	}
	wrongProofAck := httptest.NewRecorder()
	handler.ServeHTTP(wrongProofAck, newJSONRequest(http.MethodPost, "/api/secrets/"+id+"/ack", `{"token":"`+payload.Token+`","proof":"`+base64.RawURLEncoding.EncodeToString([]byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))+`"}`))
	if wrongProofAck.Code != http.StatusConflict {
		t.Fatalf("wrong proof ack status=%d, want 409", wrongProofAck.Code)
	}

	release := httptest.NewRecorder()
	handler.ServeHTTP(release, newJSONRequest(http.MethodPost, "/api/secrets/"+id+"/release", `{"token":"`+payload.Token+`"}`))
	if release.Code != http.StatusNoContent {
		t.Fatalf("release status=%d, want 204", release.Code)
	}

	second := get()
	if second.Code != http.StatusOK {
		t.Fatalf("claim after release: %d", second.Code)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	ack := httptest.NewRecorder()
	handler.ServeHTTP(ack, newJSONRequest(http.MethodPost, "/api/secrets/"+id+"/ack", `{"token":"`+payload.Token+`","proof":"`+validProof()+`"}`))
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack status=%d, want 204", ack.Code)
	}
	if got := get().Code; got != http.StatusNotFound {
		t.Fatalf("claim after ack status=%d, want 404", got)
	}
}

func TestClaimEndpointDoesNotMutateOnGETOrHEAD(t *testing.T) {
	store := newSecretStore(time.Hour)
	payload := validPayload()
	id, _, err := store.create(payload.Ciphertext, payload.IV, payload.ProofHash)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&server{store: store}).routes()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/api/secrets/"+id+"/claim", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s claim status=%d, want 405", method, response.Code)
		}
	}
	if _, _, err := store.claim(id); err != nil {
		t.Fatalf("safe HTTP methods changed claim state: %v", err)
	}
}

func TestCreateEndpointValidatesAndReturnsOpaqueID(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	body, _ := json.Marshal(validPayload())
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, response.Code, response.Body)
	}
	var result createResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.ID) != 32 {
		t.Fatalf("expected 32-character opaque ID, got %q", result.ID)
	}
	if len(result.TrackingToken) != 43 {
		t.Fatalf("expected a 43-character tracking token, got %q", result.TrackingToken)
	}
}

func TestCreateEndpointSupportsPerLinkTTLAndRejectsOutOfRange(t *testing.T) {
	store := newSecretStore(time.Hour)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	handler := (&server{store: store}).routes()
	payload, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(payload, &input); err != nil {
		t.Fatal(err)
	}
	input["ttlSeconds"] = json.RawMessage("300")
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newJSONRequest(http.MethodPost, "/api/secrets", string(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("custom TTL status=%d, want 201: %s", response.Code, response.Body)
	}
	var result createResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.ExpiresAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("expiresAt=%s, want %s", result.ExpiresAt, now.Add(5*time.Minute))
	}
	input["ttlSeconds"] = json.RawMessage("59")
	body, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, newJSONRequest(http.MethodPost, "/api/secrets", string(body)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range TTL status=%d, want 400", response.Code)
	}
}

func TestCreateEndpointRateLimitReturns429AndRetryAfter(t *testing.T) {
	service := &server{
		store:             newSecretStore(time.Hour),
		createRateLimiter: newClientRateLimiter(1, 1),
	}
	handler := service.routes()
	body, err := json.Marshal(validPayload())
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, newJSONRequest(http.MethodPost, "/api/secrets", string(body)))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status=%d, want 201", first.Code)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, newJSONRequest(http.MethodPost, "/api/secrets", string(body)))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("limited create status=%d Retry-After=%q", second.Code, second.Header().Get("Retry-After"))
	}
}

func TestCreateAtCapacityReturns503(t *testing.T) {
	store := newSecretStore(time.Hour)
	payload := validPayload()
	for range maxActiveSecrets {
		if _, _, err := store.create(payload.Ciphertext, payload.IV, payload.ProofHash); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	service := &server{store: store}
	response := httptest.NewRecorder()
	service.routes().ServeHTTP(response, newJSONRequest(http.MethodPost, "/api/secrets", string(body)))
	if response.Code != http.StatusServiceUnavailable || service.capacityRejected.Load() != 1 {
		t.Fatalf("full-store response status=%d capacity rejections=%d", response.Code, service.capacityRejected.Load())
	}
}

func TestCreateEndpointRequiresJSONContentType(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content-type status=%d, want 415", response.Code)
	}
}

func TestTrackingEndpointRequiresManagementToken(t *testing.T) {
	store := newSecretStore(time.Hour)
	payload := validPayload()
	id, _, trackingToken, err := store.createWithTTL(payload.Ciphertext, payload.IV, payload.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&server{store: store}).routes()
	request := httptest.NewRequest(http.MethodGet, "/api/secrets/"+id+"/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing tracking token status=%d, want 401", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/secrets/"+id+"/status", nil)
	request.Header.Set("Authorization", "Bearer wrong-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("wrong tracking token status=%d, want 404", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/secrets/"+id+"/status", nil)
	request.Header.Set("Authorization", "Bearer "+trackingToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), trackingToken) {
		t.Fatalf("valid tracking response status=%d or leaked token: %s", response.Code, response.Body)
	}
}

func TestMetricsRequireTokenAndExposeOnlyAggregateCounters(t *testing.T) {
	metricsToken := strings.Repeat("m", 32)
	service := &server{
		store:            newSecretStore(time.Hour),
		startedAt:        time.Now(),
		metricsEnabled:   true,
		metricsTokenHash: sha256.Sum256([]byte(metricsToken)),
	}
	payload := validPayload()
	_, _, _, err := service.store.createWithTTL(payload.Ciphertext, payload.IV, payload.ProofHash, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := service.routes()
	request := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing metrics token status=%d, want 401", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+metricsToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized metrics status=%d, want 200", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"createdTotal":1`) || strings.Contains(response.Body.String(), payload.Ciphertext) {
		t.Fatalf("metrics omitted aggregate usage or exposed encrypted payload: %s", response.Body)
	}
}

func TestClientRateLimiterAndTrustedProxyResolution(t *testing.T) {
	limiter := newClientRateLimiter(60, 2)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for range 2 {
		if allowed, _ := limiter.allow("192.0.2.1"); !allowed {
			t.Fatal("burst token was rejected")
		}
	}
	if allowed, retryAfter := limiter.allow("192.0.2.1"); allowed || retryAfter <= 0 {
		t.Fatalf("rate limit result allowed=%t retryAfter=%s", allowed, retryAfter)
	}
	now = now.Add(time.Second)
	if allowed, _ := limiter.allow("192.0.2.1"); !allowed {
		t.Fatal("request was not admitted after a token refilled")
	}

	trusted, err := parseTrustedProxies("192.0.2.10, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.10:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.7, 10.1.2.3")
	if got := clientAddress(request, trusted); got != "203.0.113.7" {
		t.Fatalf("trusted forwarded address=%q, want 203.0.113.7", got)
	}
	request.RemoteAddr = "198.51.100.4:443"
	request.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := clientAddress(request, trusted); got != "198.51.100.4" {
		t.Fatalf("untrusted forwarded address=%q, want 198.51.100.4", got)
	}
}

func TestAPIConcurrencyLimitRejectsExcessWork(t *testing.T) {
	server := &server{}
	started := make(chan struct{}, maxConcurrentAPIRequests)
	block := make(chan struct{})
	handler := limitAPIConcurrency(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-block
		w.WriteHeader(http.StatusNoContent)
	}), server)
	var wait sync.WaitGroup
	for range maxConcurrentAPIRequests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/test", nil))
		}()
	}
	for range maxConcurrentAPIRequests {
		<-started
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/test", nil))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("overload response status=%d Retry-After=%q", response.Code, response.Header().Get("Retry-After"))
	}
	close(block)
	wait.Wait()
}

func TestCreateEndpointRejectsMalformedPayload(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	request := newJSONRequest(http.MethodPost, "/api/secrets", `{"ciphertext":"plaintext","iv":"bad"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d", http.StatusBadRequest, response.Code)
	}
	payload := validPayload()
	payload.ProofHash = "invalid"
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/secrets", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid proof hash status=%d, want 400", response.Code)
	}
}

func TestSecretStoreRejectsInvalidPayloads(t *testing.T) {
	store := newSecretStore(time.Hour)
	for _, tc := range []struct{ ciphertext, iv string }{
		{"", "aXY"}, {"not base64!", "aXY"},
		{base64.RawURLEncoding.EncodeToString([]byte("short")), "aXY"},
		{base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")), "short"},
	} {
		if _, _, err := store.create(tc.ciphertext, tc.iv, validPayload().ProofHash); err == nil {
			t.Errorf("create(%q, %q) unexpectedly succeeded", tc.ciphertext, tc.iv)
		}
	}
	p := validPayload()
	if _, _, err := store.create(p.Ciphertext, p.IV, "not-a-sha256-digest"); err == nil {
		t.Fatal("expected invalid proof hash to be rejected")
	}
}

func TestSecretStoreCapacityAndExpiredCleanup(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	p := validPayload()
	for range maxActiveSecrets {
		if _, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash); err != nil {
			t.Fatalf("fill store: %v", err)
		}
	}
	if _, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash); err == nil {
		t.Fatal("expected full store error")
	}
	now = now.Add(time.Minute)
	if _, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash); err != nil {
		t.Fatalf("expired entries should be cleaned: %v", err)
	}
}

func TestConfiguredActiveCapacityIsEnforced(t *testing.T) {
	store := newSecretStoreWithLimit(time.Hour, 1)
	payload := validPayload()
	if _, _, err := store.create(payload.Ciphertext, payload.IV, payload.ProofHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.create(payload.Ciphertext, payload.IV, payload.ProofHash); !errors.Is(err, errSecretStoreFull) {
		t.Fatalf("second create error=%v, want store full", err)
	}
	if got := store.metrics().Active; got != 1 {
		t.Fatalf("active metric=%d, want 1", got)
	}
}

func TestIntegerEnvironmentEnforcesConfiguredBounds(t *testing.T) {
	const name = "TEST_ACTIVE_SECRET_LIMIT"
	t.Setenv(name, "700")
	if got, err := integerEnvironment(name, 500, 1, maxConfiguredActiveSecrets); err != nil || got != 700 {
		t.Fatalf("maximum active limit=%d, err=%v", got, err)
	}
	t.Setenv(name, "701")
	if _, err := integerEnvironment(name, 500, 1, maxConfiguredActiveSecrets); err == nil {
		t.Fatal("active limit above configured maximum was accepted")
	}
}

func TestCreateEndpointRejectsUnknownAndOversizedRequests(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	for _, testCase := range []struct {
		body string
		want int
	}{
		{body: `{"ciphertext":"x","iv":"y","extra":true}`, want: http.StatusBadRequest},
		{body: `{"ciphertext":"` + strings.Repeat("A", maxPayloadBytes) + `","iv":"aXY","proofHash":"x"}`, want: http.StatusRequestEntityTooLarge},
	} {
		req := newJSONRequest(http.MethodPost, "/api/secrets", testCase.body)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != testCase.want {
			t.Errorf("expected %d, got %d", testCase.want, res.Code)
		}
	}
}

func newJSONRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestSecurityHeadersAndStaticPage(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /: %d", res.Code)
	}
	for key, want := range map[string]string{
		"Cache-Control":                     "no-store",
		"X-Content-Type-Options":            "nosniff",
		"Referrer-Policy":                   "no-referrer",
		"X-Frame-Options":                   "DENY",
		"X-Permitted-Cross-Domain-Policies": "none",
		"Cross-Origin-Opener-Policy":        "same-origin",
		"Cross-Origin-Resource-Policy":      "same-origin",
		"Permissions-Policy":                "camera=(), microphone=(), geolocation=()",
	} {
		if got := res.Header().Get(key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
}

func TestPWAAssetsDeclareNetworkOnlyBehavior(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	if manifestResponse.Code != http.StatusOK || manifestResponse.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest response status=%d content-type=%q", manifestResponse.Code, manifestResponse.Header().Get("Content-Type"))
	}
	var manifest struct {
		Icons []struct {
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	iconSizes := make(map[string]bool)
	for _, icon := range manifest.Icons {
		iconSizes[icon.Sizes] = true
	}
	if !iconSizes["192x192"] || !iconSizes["512x512"] {
		t.Fatalf("PWA manifest does not provide 192/512 icons: %+v", iconSizes)
	}
	for _, iconPath := range []string{"/icon-192.png", "/icon-512.png"} {
		iconResponse := httptest.NewRecorder()
		handler.ServeHTTP(iconResponse, httptest.NewRequest(http.MethodGet, iconPath, nil))
		if iconResponse.Code != http.StatusOK || !strings.HasPrefix(iconResponse.Body.String(), "\x89PNG\r\n\x1a\n") {
			t.Errorf("PWA icon %s was not served as PNG: status=%d", iconPath, iconResponse.Code)
		}
	}
	workerResponse := httptest.NewRecorder()
	handler.ServeHTTP(workerResponse, httptest.NewRequest(http.MethodGet, "/service-worker.js", nil))
	if workerResponse.Code != http.StatusOK || !strings.Contains(workerResponse.Body.String(), "event.respondWith(fetch(event.request))") {
		t.Fatalf("network-only service worker response status=%d", workerResponse.Code)
	}
	if strings.Contains(workerResponse.Body.String(), "caches.open") || strings.Contains(workerResponse.Body.String(), "cache.addAll") {
		t.Fatal("service worker unexpectedly contains offline cache behavior")
	}
}

func TestHealthEndpoint(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("GET /healthz status=%d, want %d", response.Code, http.StatusNoContent)
	}
}

func BenchmarkSecretStoreCreate(b *testing.B) {
	p := validPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := newSecretStore(time.Hour).create(p.Ciphertext, p.IV, p.ProofHash); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSecretStoreConsume(b *testing.B) {
	p := validPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		store := newSecretStore(time.Hour)
		id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
		if err != nil {
			b.Fatal(err)
		}
		_, token, err := store.claim(id)
		if err != nil {
			b.Fatal(err)
		}
		if err := store.acknowledge(id, token, validProof()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSecretStoreConcurrentConsume(b *testing.B) {
	p := validPayload()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			store := newSecretStore(time.Hour)
			id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
			if err != nil {
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
