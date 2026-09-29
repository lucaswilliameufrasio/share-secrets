package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
