package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func validPayload() createRequest {
	return createRequest{
		Ciphertext: base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")),
		IV:         base64.RawURLEncoding.EncodeToString([]byte("0123456789ab")),
	}
}

func TestSecretCanOnlyBeConsumedOnce(t *testing.T) {
	store := newSecretStore(time.Hour)
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	id, _, err := store.create(validPayload().Ciphertext, validPayload().IV)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if _, ok := store.consume(id); !ok {
		t.Fatal("first consume should return the encrypted secret")
	}
	if _, ok := store.consume(id); ok {
		t.Fatal("second consume should not return the secret")
	}
}

func TestExpiredSecretCannotBeConsumed(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	id, _, err := store.create(validPayload().Ciphertext, validPayload().IV)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	now = now.Add(time.Minute)
	if _, ok := store.consume(id); ok {
		t.Fatal("expired secret should not be returned")
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
}

func TestConcurrentConsumeReturnsSecretToSingleCaller(t *testing.T) {
	store := newSecretStore(time.Hour)
	id, _, err := store.create(validPayload().Ciphertext, validPayload().IV)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	results := make(chan bool, 2)
	for range 2 {
		go func() { _, ok := store.consume(id); results <- ok }()
	}
	if first, second := <-results, <-results; first == second {
		t.Fatalf("expected exactly one successful consume, got %v and %v", first, second)
	}
}

func TestSecretStoreRejectsInvalidPayloads(t *testing.T) {
	store := newSecretStore(time.Hour)
	for _, tc := range []struct{ ciphertext, iv string }{
		{"", "aXY"}, {"not base64!", "aXY"},
		{base64.RawURLEncoding.EncodeToString([]byte("short")), "aXY"},
		{base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef")), "short"},
	} {
		if _, _, err := store.create(tc.ciphertext, tc.iv); err == nil {
			t.Errorf("create(%q, %q) unexpectedly succeeded", tc.ciphertext, tc.iv)
		}
	}
}

func TestSecretStoreCapacityAndExpiredCleanup(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	p := validPayload()
	for range maxActiveSecrets {
		if _, _, err := store.create(p.Ciphertext, p.IV); err != nil {
			t.Fatalf("fill store: %v", err)
		}
	}
	if _, _, err := store.create(p.Ciphertext, p.IV); err == nil {
		t.Fatal("expected full store error")
	}
	now = now.Add(time.Minute)
	if _, _, err := store.create(p.Ciphertext, p.IV); err != nil {
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
		if _, _, err := newSecretStore(time.Hour).create(p.Ciphertext, p.IV); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSecretStoreConsume(b *testing.B) {
	p := validPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		store := newSecretStore(time.Hour)
		id, _, err := store.create(p.Ciphertext, p.IV)
		if err != nil {
			b.Fatal(err)
		}
		if _, ok := store.consume(id); !ok {
			b.Fatal("consume failed")
		}
	}
}
