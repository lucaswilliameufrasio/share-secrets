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
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/secrets/"+id, nil))
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
	handler.ServeHTTP(badAck, httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/ack", strings.NewReader(`{"token":"wrong"}`)))
	if badAck.Code != http.StatusConflict {
		t.Fatalf("bad ack status=%d, want 409", badAck.Code)
	}
	wrongProofAck := httptest.NewRecorder()
	handler.ServeHTTP(wrongProofAck, httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/ack", strings.NewReader(`{"token":"`+payload.Token+`","proof":"`+base64.RawURLEncoding.EncodeToString([]byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"))+`"}`)))
	if wrongProofAck.Code != http.StatusConflict {
		t.Fatalf("wrong proof ack status=%d, want 409", wrongProofAck.Code)
	}

	release := httptest.NewRecorder()
	handler.ServeHTTP(release, httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/release", strings.NewReader(`{"token":"`+payload.Token+`"}`)))
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
	handler.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/api/secrets/"+id+"/ack", strings.NewReader(`{"token":"`+payload.Token+`","proof":"`+validProof()+`"}`)))
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack status=%d, want 204", ack.Code)
	}
	if got := get().Code; got != http.StatusNotFound {
		t.Fatalf("claim after ack status=%d, want 404", got)
	}
}

func TestCreateEndpointValidatesAndReturnsOpaqueID(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	body, _ := json.Marshal(validPayload())
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, response.Code, response.Body)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.ID) != 32 {
		t.Fatalf("expected 32-character opaque ID, got %q", result.ID)
	}
}

func TestCreateEndpointRejectsMalformedPayload(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	request := httptest.NewRequest(http.MethodPost, "/api/secrets", bytes.NewBufferString(`{"ciphertext":"plaintext","iv":"bad"}`))
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

func TestCreateEndpointRejectsUnknownAndOversizedRequests(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	for _, body := range []string{`{"ciphertext":"x","iv":"y","extra":true}`, strings.Repeat("x", maxPayloadBytes+1)} {
		req := httptest.NewRequest(http.MethodPost, "/api/secrets", strings.NewReader(body))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", res.Code)
		}
	}
}

func TestSecurityHeadersAndStaticPage(t *testing.T) {
	handler := (&server{store: newSecretStore(time.Hour)}).routes()
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /: %d", res.Code)
	}
	for key, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY"} {
		if got := res.Header().Get(key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
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
