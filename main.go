package main

import (
	"crypto/rand"
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
)

//go:embed web
var webFiles embed.FS

type encryptedSecret struct {
	Ciphertext string    `json:"ciphertext"`
	IV         string    `json:"iv"`
	ExpiresAt  time.Time `json:"expiresAt"`
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

func (s *secretStore) create(ciphertext, iv string) (string, time.Time, error) {
	if ciphertext == "" || iv == "" {
		return "", time.Time{}, errors.New("ciphertext and iv are required")
	}
	ciphertextBytes, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil || len(ciphertextBytes) < 16 {
		return "", time.Time{}, errors.New("ciphertext must be base64url")
	}
	ivBytes, err := base64.RawURLEncoding.DecodeString(iv)
	if err != nil || len(ivBytes) != 12 {
		return "", time.Time{}, errors.New("iv must be base64url")
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
	s.secrets[id] = encryptedSecret{Ciphertext: ciphertext, IV: iv, ExpiresAt: expiresAt}
	s.mu.Unlock()
	return id, expiresAt, nil
}

func (s *secretStore) consume(id string) (encryptedSecret, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.secrets[id]
	if !ok {
		return encryptedSecret{}, false
	}
	delete(s.secrets, id)
	if !item.ExpiresAt.After(s.now()) {
		return encryptedSecret{}, false
	}
	return item, true
}

type createRequest struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
}

type server struct {
	store *secretStore
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/secrets", s.createSecret)
	mux.HandleFunc("GET /api/secrets/{id}", s.consumeSecret)
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
	id, expiresAt, err := s.store.create(input.Ciphertext, input.IV)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid encrypted payload")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "expiresAt": expiresAt})
}

func (s *server) consumeSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) != 32 || strings.ContainsAny(id, "/. ") {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	item, ok := s.store.consume(id)
	if !ok {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(item)
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
