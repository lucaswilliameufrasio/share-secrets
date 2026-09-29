package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	maxPayloadBytes  = 64 * 1024
	maxActiveSecrets = 500
	defaultTTL       = time.Hour
	claimLease       = 30 * time.Second
)

var (
	errSecretUnavailable = errors.New("secret unavailable")
	errSecretClaimed     = errors.New("secret already claimed")
	errInvalidClaim      = errors.New("invalid claim")
	errInvalidProof      = errors.New("invalid decryption proof")
)

//go:embed web
var webFiles embed.FS

type encryptedSecret struct {
	Ciphertext string    `json:"ciphertext"`
	IV         string    `json:"iv"`
	ExpiresAt  time.Time `json:"expiresAt"`
	proofHash  string
	claimToken string
	claimUntil time.Time
}

type secretStore struct {
	mu      sync.Mutex
	secrets map[string]encryptedSecret
	ttl     time.Duration
	now     func() time.Time
}

func newSecretStore(ttl time.Duration) *secretStore {
	return &secretStore{secrets: make(map[string]encryptedSecret), ttl: ttl, now: time.Now}
}

func (s *secretStore) create(ciphertext, iv, proofHash string) (string, time.Time, error) {
	if ciphertext == "" || iv == "" || proofHash == "" {
		return "", time.Time{}, errors.New("ciphertext, iv and proof hash are required")
	}
	ciphertextBytes, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil || len(ciphertextBytes) < 16 {
		return "", time.Time{}, errors.New("ciphertext must be base64url")
	}
	ivBytes, err := base64.RawURLEncoding.DecodeString(iv)
	if err != nil || len(ivBytes) != 12 {
		return "", time.Time{}, errors.New("iv must be base64url")
	}
	proofHashBytes, err := base64.RawURLEncoding.DecodeString(proofHash)
	if err != nil || len(proofHashBytes) != sha256.Size {
		return "", time.Time{}, errors.New("proof hash must be a SHA-256 base64url digest")
	}

	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return "", time.Time{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(token)
	expiresAt := s.now().Add(s.ttl)
	s.mu.Lock()
	for existingID, item := range s.secrets {
		if !item.ExpiresAt.After(s.now()) {
			delete(s.secrets, existingID)
		}
	}
	if len(s.secrets) >= maxActiveSecrets {
		s.mu.Unlock()
		return "", time.Time{}, errors.New("secret store is full")
	}
	s.secrets[id] = encryptedSecret{Ciphertext: ciphertext, IV: iv, ExpiresAt: expiresAt, proofHash: proofHash}
	s.mu.Unlock()
	return id, expiresAt, nil
}

func (s *secretStore) claim(id string) (encryptedSecret, string, error) {
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return encryptedSecret{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.secrets[id]
	if !ok || !item.ExpiresAt.After(s.now()) {
		delete(s.secrets, id)
		return encryptedSecret{}, "", errSecretUnavailable
	}
	if item.claimToken != "" && item.claimUntil.After(s.now()) {
		return encryptedSecret{}, "", errSecretClaimed
	}
	item.claimToken = token
	item.claimUntil = s.now().Add(claimLease)
	s.secrets[id] = item
	return item, token, nil
}

func (s *secretStore) acknowledge(id, token, proof string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.secrets[id]
	if !ok || !item.ExpiresAt.After(s.now()) {
		delete(s.secrets, id)
		return errSecretUnavailable
	}
	if !validClaim(item, token, s.now()) {
		return errInvalidClaim
	}
	proofBytes, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(proofBytes) != 32 {
		return errInvalidProof
	}
	proofHash, err := base64.RawURLEncoding.DecodeString(item.proofHash)
	if err != nil || len(proofHash) != sha256.Size {
		return errInvalidProof
	}
	actualHash := sha256.Sum256(proofBytes)
	if subtle.ConstantTimeCompare(actualHash[:], proofHash) != 1 {
		return errInvalidProof
	}
	delete(s.secrets, id)
	return nil
}

func (s *secretStore) release(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.secrets[id]
	if !ok || !item.ExpiresAt.After(s.now()) {
		delete(s.secrets, id)
		return errSecretUnavailable
	}
	if !validClaim(item, token, s.now()) {
		return errInvalidClaim
	}
	item.claimToken = ""
	item.claimUntil = time.Time{}
	s.secrets[id] = item
	return nil
}

func validClaim(item encryptedSecret, token string, now time.Time) bool {
	return token != "" && item.claimToken != "" && item.claimUntil.After(now) &&
		subtle.ConstantTimeCompare([]byte(item.claimToken), []byte(token)) == 1
}

type createRequest struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
	ProofHash  string `json:"proofHash"`
}

type claimRequest struct {
	Token string `json:"token"`
	Proof string `json:"proof,omitempty"`
}

type claimResponse struct {
	Ciphertext string    `json:"ciphertext"`
	IV         string    `json:"iv"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Token      string    `json:"token"`
}

type server struct {
	store *secretStore
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/secrets", s.createSecret)
	mux.HandleFunc("GET /api/secrets/{id}", s.claimSecret)
	mux.HandleFunc("POST /api/secrets/{id}/ack", s.acknowledgeSecret)
	mux.HandleFunc("POST /api/secrets/{id}/release", s.releaseSecret)
	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return securityHeaders(mux)
}

func (s *server) createSecret(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPayloadBytes)
	var input createRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	id, expiresAt, err := s.store.create(input.Ciphertext, input.IV, input.ProofHash)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid encrypted payload")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "expiresAt": expiresAt})
}

func (s *server) claimSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) != 32 || strings.ContainsAny(id, "/. ") {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	item, token, err := s.store.claim(id)
	if errors.Is(err, errSecretClaimed) {
		writeError(w, http.StatusConflict, "secret is being opened")
		return
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(claimResponse{Ciphertext: item.Ciphertext, IV: item.IV, ExpiresAt: item.ExpiresAt, Token: token})
}

func (s *server) acknowledgeSecret(w http.ResponseWriter, r *http.Request) {
	s.updateClaim(w, r, true)
}

func (s *server) releaseSecret(w http.ResponseWriter, r *http.Request) {
	s.updateClaim(w, r, false)
}

func (s *server) updateClaim(w http.ResponseWriter, r *http.Request, acknowledge bool) {
	id := r.PathValue("id")
	if len(id) != 32 || strings.ContainsAny(id, "/. ") {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input claimRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Token == "" || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid claim")
		return
	}
	var err error
	if acknowledge {
		err = s.store.acknowledge(id, input.Token, input.Proof)
	} else {
		err = s.store.release(id, input.Token)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "claim unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func main() {
	ttl := defaultTTL
	if value := os.Getenv("SECRET_TTL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute || parsed > 24*time.Hour {
			log.Fatal("SECRET_TTL must be between 1m and 24h")
		}
		ttl = parsed
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           (&server{store: newSecretStore(ttl)}).routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Print("server started")
	log.Fatal(srv.ListenAndServe())
}
