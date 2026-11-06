package main

import (
	"context"
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
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	maxPayloadBytes            = 64 * 1024
	maxActiveSecrets           = 500
	maxConfiguredActiveSecrets = 700
	maxTrackedSecrets          = 10_000
	maxRateLimitClients        = 10_000
	maxConcurrentAPIRequests   = 256
	maxHeaderBytes             = 16 * 1024
	defaultTTL                 = time.Hour
	minimumTTL                 = time.Minute
	maximumTTL                 = 24 * time.Hour
	claimLease                 = 30 * time.Second
	trackingRetention          = 7 * 24 * time.Hour
	defaultCreateRatePerMinute = 60
	defaultCreateRateBurst     = 20
)

var (
	errSecretUnavailable    = errors.New("secret unavailable")
	errSecretClaimed        = errors.New("secret already claimed")
	errInvalidClaim         = errors.New("invalid claim")
	errInvalidProof         = errors.New("invalid decryption proof")
	errSecretStoreFull      = errors.New("secret store is full")
	errUnsupportedMediaType = errors.New("unsupported media type")
	errStorageFailure       = errors.New("storage failure")
)

//go:embed web
var webFiles embed.FS

type encryptedSecret struct {
	Ciphertext          string
	IV                  string
	CreatedAt           time.Time
	ExpiresAt           time.Time
	UsedAt              time.Time
	TerminalAt          time.Time
	Status              string
	ProofHash           [sha256.Size]byte
	ManagementTokenHash [sha256.Size]byte
	ClaimTokenHash      [sha256.Size]byte
	ClaimUntil          time.Time
	ClaimCount          uint64
}

type secretStore struct {
	mu            sync.RWMutex
	secrets       map[string]encryptedSecret
	activeIDs     map[string]struct{}
	expirations   deadlineQueue
	terminals     deadlineQueue
	ttl           time.Duration
	now           func() time.Time
	activeCount   int
	activeLimit   int
	createdTotal  uint64
	claimsTotal   uint64
	consumedTotal uint64
	expiredTotal  uint64
}

func newSecretStore(ttl time.Duration) *secretStore {
	return newSecretStoreWithLimit(ttl, maxActiveSecrets)
}

func newSecretStoreWithLimit(ttl time.Duration, activeLimit int) *secretStore {
	return &secretStore{secrets: make(map[string]encryptedSecret), activeIDs: make(map[string]struct{}), ttl: ttl, now: time.Now, activeLimit: activeLimit}
}

func (s *secretStore) create(ciphertext, iv, proofHash string) (string, time.Time, error) {
	id, expiresAt, _, err := s.createWithTTL(ciphertext, iv, proofHash, s.ttl)
	return id, expiresAt, err
}

func (s *secretStore) createWithTTL(ciphertext, iv, proofHash string, ttl time.Duration) (string, time.Time, string, error) {
	if ciphertext == "" || iv == "" || proofHash == "" {
		return "", time.Time{}, "", errors.New("ciphertext, iv and proof hash are required")
	}
	ciphertextBytes, err := base64.RawURLEncoding.DecodeString(ciphertext)
	if err != nil || len(ciphertextBytes) < 16 {
		return "", time.Time{}, "", errors.New("ciphertext must be base64url")
	}
	ivBytes, err := base64.RawURLEncoding.DecodeString(iv)
	if err != nil || len(ivBytes) != 12 {
		return "", time.Time{}, "", errors.New("iv must be base64url")
	}
	proofHashBytes, err := base64.RawURLEncoding.DecodeString(proofHash)
	if err != nil || len(proofHashBytes) != sha256.Size {
		return "", time.Time{}, "", errors.New("proof hash must be a SHA-256 base64url digest")
	}
	if ttl < minimumTTL || ttl > maximumTTL {
		return "", time.Time{}, "", errors.New("ttl must be between 1m and 24h")
	}

	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return "", time.Time{}, "", err
	}
	managementTokenBytes := make([]byte, 32)
	if _, err := rand.Read(managementTokenBytes); err != nil {
		return "", time.Time{}, "", err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	managementToken := base64.RawURLEncoding.EncodeToString(managementTokenBytes)
	managementTokenHash := sha256.Sum256([]byte(managementToken))
	now := s.now()
	expiresAt := now.Add(ttl)
	s.mu.Lock()
	s.cleanupLocked(now)
	if s.activeCount >= s.activeLimit {
		s.mu.Unlock()
		return "", time.Time{}, "", errSecretStoreFull
	}
	if len(s.secrets) >= maxTrackedSecrets {
		s.evictOldestTerminalLocked()
	}
	if len(s.secrets) >= maxTrackedSecrets {
		s.mu.Unlock()
		return "", time.Time{}, "", errSecretStoreFull
	}
	var parsedProofHash [sha256.Size]byte
	copy(parsedProofHash[:], proofHashBytes)
	s.secrets[id] = encryptedSecret{
		Ciphertext:          ciphertext,
		IV:                  iv,
		CreatedAt:           now,
		ExpiresAt:           expiresAt,
		ProofHash:           parsedProofHash,
		ManagementTokenHash: managementTokenHash,
	}
	s.activeCount++
	s.activeIDs[id] = struct{}{}
	s.expirations.add(id, expiresAt)
	s.createdTotal++
	s.mu.Unlock()
	return id, expiresAt, managementToken, nil
}

func (s *secretStore) claim(id string) (encryptedSecret, string, error) {
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return encryptedSecret{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expireOneLocked(id, now)
	item, ok := s.secrets[id]
	if !ok || item.Status != "" {
		return encryptedSecret{}, "", errSecretUnavailable
	}
	if item.ClaimUntil.After(now) && item.ClaimTokenHash != ([sha256.Size]byte{}) {
		return encryptedSecret{}, "", errSecretClaimed
	}
	item.ClaimTokenHash = tokenHash
	item.ClaimUntil = now.Add(claimLease)
	item.ClaimCount++
	s.secrets[id] = item
	s.claimsTotal++
	return item, token, nil
}

func (s *secretStore) acknowledge(id, token, proof string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expireOneLocked(id, now)
	item, ok := s.secrets[id]
	if !ok || item.Status != "" {
		return errSecretUnavailable
	}
	if !validClaim(item, token, now) {
		return errInvalidClaim
	}
	proofBytes, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(proofBytes) != 32 {
		return errInvalidProof
	}
	actualHash := sha256.Sum256(proofBytes)
	if subtle.ConstantTimeCompare(actualHash[:], item.ProofHash[:]) != 1 {
		return errInvalidProof
	}
	item.Status = "used"
	item.UsedAt = now
	item.TerminalAt = now
	item.clearEncryptedPayload()
	s.secrets[id] = item
	s.activeCount--
	delete(s.activeIDs, id)
	s.expirations.remove(id)
	s.terminals.add(id, now)
	s.consumedTotal++
	return nil
}

func (s *secretStore) release(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expireOneLocked(id, now)
	item, ok := s.secrets[id]
	if !ok || item.Status != "" {
		return errSecretUnavailable
	}
	if !validClaim(item, token, now) {
		return errInvalidClaim
	}
	item.ClaimTokenHash = [sha256.Size]byte{}
	item.ClaimUntil = time.Time{}
	s.secrets[id] = item
	return nil
}

func validClaim(item encryptedSecret, token string, now time.Time) bool {
	if token == "" || item.ClaimTokenHash == ([sha256.Size]byte{}) || !item.ClaimUntil.After(now) {
		return false
	}
	tokenHash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(item.ClaimTokenHash[:], tokenHash[:]) == 1
}

func (item *encryptedSecret) clearEncryptedPayload() {
	item.Ciphertext = ""
	item.IV = ""
	item.ProofHash = [sha256.Size]byte{}
	item.ClaimTokenHash = [sha256.Size]byte{}
	item.ClaimUntil = time.Time{}
}

func (s *secretStore) cleanupLocked(now time.Time) {
	for i := 0; i < 64; i++ {
		entry := s.expirations.first()
		if entry == nil || entry.at.After(now) {
			break
		}
		s.expireOneLocked(entry.id, now)
	}
	for i := 0; i < 64; i++ {
		entry := s.terminals.first()
		if entry == nil || entry.at.Add(trackingRetention).After(now) {
			break
		}
		s.evictOldestTerminalLocked()
	}
}

func (s *secretStore) expireOneLocked(id string, now time.Time) {
	item, ok := s.secrets[id]
	if !ok || item.Status != "" || item.ExpiresAt.After(now) {
		return
	}
	item.Status = "expired"
	item.TerminalAt = item.ExpiresAt
	item.clearEncryptedPayload()
	s.secrets[id] = item
	delete(s.activeIDs, id)
	s.expirations.remove(id)
	s.terminals.add(id, item.TerminalAt)
	s.activeCount--
	s.expiredTotal++
}

func (s *secretStore) evictOldestTerminalLocked() {
	if entry := s.terminals.first(); entry != nil {
		delete(s.secrets, entry.id)
		s.terminals.remove(entry.id)
	}
}

type trackingStatus struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	UsedAt     *time.Time `json:"usedAt,omitempty"`
	ClaimCount uint64     `json:"claimCount"`
}

func (s *secretStore) status(id, token string) (trackingStatus, error) {
	s.mu.RLock()
	now := s.now()
	item, ok := s.secrets[id]
	s.mu.RUnlock()
	if ok && item.Status == "" && !item.ExpiresAt.After(now) {
		s.mu.Lock()
		s.expireOneLocked(id, now)
		item, ok = s.secrets[id]
		s.mu.Unlock()
	}
	if ok && item.Status != "" && !item.TerminalAt.Add(trackingRetention).After(now) {
		return trackingStatus{}, errSecretUnavailable
	}
	if !ok || token == "" {
		return trackingStatus{}, errSecretUnavailable
	}
	tokenHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(item.ManagementTokenHash[:], tokenHash[:]) != 1 {
		return trackingStatus{}, errSecretUnavailable
	}
	status := item.Status
	if status == "" {
		status = "active"
		if item.ClaimUntil.After(now) && item.ClaimTokenHash != ([sha256.Size]byte{}) {
			status = "opening"
		}
	}
	result := trackingStatus{
		ID:         id,
		Status:     status,
		CreatedAt:  item.CreatedAt,
		ExpiresAt:  item.ExpiresAt,
		ClaimCount: item.ClaimCount,
	}
	if !item.UsedAt.IsZero() {
		usedAt := item.UsedAt
		result.UsedAt = &usedAt
	}
	return result, nil
}

type storeMetrics struct {
	CreatedTotal           uint64 `json:"createdTotal"`
	ClaimsTotal            uint64 `json:"claimsTotal"`
	ConsumedTotal          uint64 `json:"consumedTotal"`
	ExpiredTotal           uint64 `json:"expiredTotal"`
	Active                 int    `json:"active"`
	Opening                int    `json:"opening"`
	Tracked                int    `json:"tracked"`
	CleanupPending         int    `json:"cleanupPending"`
	QueueDepth             int    `json:"storageQueueDepth"`
	QueueCapacity          int    `json:"storageQueueCapacity"`
	QueueRejectedTotal     uint64 `json:"storageQueueRejectedTotal"`
	BatchBeginFailures     uint64 `json:"storageBatchBeginFailures"`
	BatchOperationFailures uint64 `json:"storageBatchOperationFailures"`
	BatchSavepointFailures uint64 `json:"storageBatchSavepointFailures"`
	BatchCommitFailures    uint64 `json:"storageBatchCommitFailures"`
	QueueExpiredTotal      uint64 `json:"storageQueueExpiredTotal"`
	BatchBusyErrors        uint64 `json:"storageBatchBusyErrors"`
	BatchIOErrors          uint64 `json:"storageBatchIOErrors"`
	BatchLastIOCode        int64  `json:"storageBatchLastIOCode"`
	BatchInterruptErrors   uint64 `json:"storageBatchInterruptErrors"`
	BatchConstraintErrors  uint64 `json:"storageBatchConstraintErrors"`
	BatchOtherErrors       uint64 `json:"storageBatchOtherErrors"`
}

func (s *secretStore) metrics() storeMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.cleanupLocked(now)
	// At most 700 active entries; metrics stay exact even after simultaneous
	// expiry, without scanning the retained terminal history.
	for entry := s.expirations.first(); entry != nil && !entry.at.After(now); entry = s.expirations.first() {
		s.expireOneLocked(entry.id, now)
	}
	metrics := storeMetrics{
		CreatedTotal:  s.createdTotal,
		ClaimsTotal:   s.claimsTotal,
		ConsumedTotal: s.consumedTotal,
		ExpiredTotal:  s.expiredTotal,
		Active:        s.activeCount,
		Tracked:       len(s.secrets),
	}
	for id := range s.activeIDs {
		item := s.secrets[id]
		if item.Status == "" && item.ClaimUntil.After(now) && item.ClaimTokenHash != ([sha256.Size]byte{}) {
			metrics.Opening++
		}
	}
	return metrics
}

func (s *secretStore) cleanupExpired() {
	s.mu.Lock()
	s.cleanupLocked(s.now())
	s.mu.Unlock()
}

type rateBucket struct {
	tokens     float64
	updatedAt  time.Time
	lastSeenAt time.Time
}

type clientRateLimiter struct {
	mu            sync.Mutex
	buckets       map[string]rateBucket
	ratePerSecond float64
	burst         float64
	now           func() time.Time
	lastCleanupAt time.Time
}

func newClientRateLimiter(perMinute, burst int) *clientRateLimiter {
	return &clientRateLimiter{
		buckets:       make(map[string]rateBucket),
		ratePerSecond: float64(perMinute) / 60,
		burst:         float64(burst),
		now:           time.Now,
	}
}

func (l *clientRateLimiter) allow(client string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastCleanupAt.IsZero() || now.Sub(l.lastCleanupAt) >= time.Minute {
		for key, bucket := range l.buckets {
			if now.Sub(bucket.lastSeenAt) >= time.Hour {
				delete(l.buckets, key)
			}
		}
		l.lastCleanupAt = now
	}
	bucket, exists := l.buckets[client]
	if !exists {
		if len(l.buckets) >= maxRateLimitClients {
			return false, time.Minute
		}
		bucket = rateBucket{tokens: l.burst, updatedAt: now}
	} else {
		elapsed := now.Sub(bucket.updatedAt).Seconds()
		bucket.tokens = math.Min(l.burst, bucket.tokens+elapsed*l.ratePerSecond)
		bucket.updatedAt = now
	}
	bucket.lastSeenAt = now
	if bucket.tokens < 1 {
		l.buckets[client] = bucket
		return false, time.Duration(math.Ceil((1 - bucket.tokens) / l.ratePerSecond * float64(time.Second)))
	}
	bucket.tokens--
	l.buckets[client] = bucket
	return true, 0
}

func parseTrustedProxies(value string) ([]*net.IPNet, error) {
	var trusted []*net.IPNet
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, errors.New("TRUSTED_PROXIES must contain IP addresses or CIDRs")
			}
			maskBits := 128
			if ip.To4() != nil {
				maskBits = 32
			}
			_, network, err = net.ParseCIDR(ip.String() + "/" + strconv.Itoa(maskBits))
			if err != nil {
				return nil, errors.New("TRUSTED_PROXIES contains an invalid IP address")
			}
		}
		trusted = append(trusted, network)
	}
	return trusted, nil
}

func clientAddress(r *http.Request, trusted []*net.IPNet) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = strings.Trim(r.RemoteAddr, "[]")
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP == nil || !isTrustedProxy(remoteIP, trusted) {
		if remoteIP != nil {
			return remoteIP.String()
		}
		return remoteHost
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	chain := make([]net.IP, 0, len(forwarded)+1)
	for _, value := range forwarded {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return remoteIP.String()
		}
		chain = append(chain, ip)
	}
	chain = append(chain, remoteIP)
	for index := len(chain) - 1; index >= 0; index-- {
		if !isTrustedProxy(chain[index], trusted) {
			return chain[index].String()
		}
	}
	return remoteIP.String()
}

func isTrustedProxy(ip net.IP, trusted []*net.IPNet) bool {
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func validSecretID(id string) bool {
	if len(id) != 32 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(decoded) == 24
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errUnsupportedMediaType
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnsupportedMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid request")
}

func limitAPIConcurrency(next http.Handler, s *server) http.Handler {
	semaphore := make(chan struct{}, maxConcurrentAPIRequests)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case semaphore <- struct{}{}:
			defer func() {
				<-semaphore
			}()
			next.ServeHTTP(w, r)
		default:
			s.overloadedTotal.Add(1)
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "service is busy")
		}
	})
}

type createRequest struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
	ProofHash  string `json:"proofHash"`
	TTLSeconds *int   `json:"ttlSeconds,omitempty"`
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

type createResponse struct {
	ID            string    `json:"id"`
	ExpiresAt     time.Time `json:"expiresAt"`
	TrackingToken string    `json:"trackingToken"`
}

type runtimeConfigResponse struct {
	DefaultTTLSeconds int64 `json:"defaultTTLSeconds"`
	MinimumTTLSeconds int64 `json:"minimumTTLSeconds"`
	MaximumTTLSeconds int64 `json:"maximumTTLSeconds"`
}

type usageMetricsResponse struct {
	storeMetrics
	RateLimitedTotal uint64 `json:"rateLimitedTotal"`
	OverloadedTotal  uint64 `json:"overloadedTotal"`
	CapacityRejected uint64 `json:"capacityRejectedTotal"`
	ActiveCapacity   int    `json:"activeCapacity"`
	UptimeSeconds    int64  `json:"uptimeSeconds"`
}

type server struct {
	store             secretStorage
	startedAt         time.Time
	trustedProxies    []*net.IPNet
	createRateLimiter *clientRateLimiter
	metricsTokenHash  [sha256.Size]byte
	metricsEnabled    bool
	rateLimitedTotal  atomic.Uint64
	overloadedTotal   atomic.Uint64
	capacityRejected  atomic.Uint64
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/config", s.getRuntimeConfig)
	mux.HandleFunc("GET /api/metrics", s.getUsageMetrics)
	mux.HandleFunc("POST /api/secrets", s.createSecret)
	mux.HandleFunc("GET /api/secrets/{id}/claim", methodNotAllowed(http.MethodPost))
	mux.HandleFunc("POST /api/secrets/{id}/claim", s.claimSecret)
	mux.HandleFunc("GET /api/secrets/{id}/status", s.getSecretStatus)
	mux.HandleFunc("POST /api/secrets/{id}/ack", s.acknowledgeSecret)
	mux.HandleFunc("POST /api/secrets/{id}/release", s.releaseSecret)
	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	manifest, err := fs.ReadFile(staticFiles, "manifest.webmanifest")
	if err != nil {
		panic(err)
	}
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		_, _ = w.Write(manifest)
	})
	mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return securityHeaders(limitAPIConcurrency(mux, s))
}

func (s *server) createSecret(w http.ResponseWriter, r *http.Request) {
	if s.createRateLimiter != nil {
		allowed, retryAfter := s.createRateLimiter.allow(clientAddress(r, s.trustedProxies))
		if !allowed {
			s.rateLimitedTotal.Add(1)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retryAfter.Seconds())))))
			writeError(w, http.StatusTooManyRequests, "creation rate limit exceeded")
			return
		}
	}
	var input createRequest
	if err := decodeJSON(w, r, maxPayloadBytes, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	ttl := s.store.defaultTTL()
	if input.TTLSeconds != nil {
		if *input.TTLSeconds < int(minimumTTL/time.Second) || *input.TTLSeconds > int(maximumTTL/time.Second) {
			writeError(w, http.StatusBadRequest, "ttlSeconds must be between 60 and 86400")
			return
		}
		ttl = time.Duration(*input.TTLSeconds) * time.Second
	}
	id, expiresAt, trackingToken, err := s.store.createWithTTL(input.Ciphertext, input.IV, input.ProofHash, ttl)
	if err != nil {
		if errors.Is(err, errSecretStoreFull) {
			s.capacityRejected.Add(1)
			writeError(w, http.StatusServiceUnavailable, "secret capacity reached")
			return
		}
		if errors.Is(err, errStorageFailure) {
			writeError(w, http.StatusServiceUnavailable, "storage unavailable")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid encrypted payload")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createResponse{ID: id, ExpiresAt: expiresAt, TrackingToken: trackingToken})
}

func (s *server) claimSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := r.PathValue("id")
	if !validSecretID(id) {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	item, token, err := s.store.claim(id)
	if errors.Is(err, errStorageFailure) {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if errors.Is(err, errSecretClaimed) {
		writeError(w, http.StatusConflict, "secret is being opened")
		return
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(claimResponse{Ciphertext: item.Ciphertext, IV: item.IV, ExpiresAt: item.ExpiresAt, Token: token})
}

func methodNotAllowed(allowedMethod string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allowedMethod)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *server) getSecretStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validSecretID(id) {
		writeError(w, http.StatusNotFound, "tracking unavailable")
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "tracking unavailable")
		return
	}
	result, err := s.store.status(id, strings.TrimPrefix(authorization, "Bearer "))
	if errors.Is(err, errStorageFailure) {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "tracking unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *server) getRuntimeConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runtimeConfigResponse{
		DefaultTTLSeconds: int64(s.store.defaultTTL() / time.Second),
		MinimumTTLSeconds: int64(minimumTTL / time.Second),
		MaximumTTLSeconds: int64(maximumTTL / time.Second),
	})
}

func (s *server) getUsageMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsEnabled {
		http.NotFound(w, r)
		return
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "metrics authorization required")
		return
	}
	tokenHash := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
	if subtle.ConstantTimeCompare(tokenHash[:], s.metricsTokenHash[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "metrics authorization required")
		return
	}
	startedAt := s.startedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	metrics, err := s.store.readMetrics()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	result := usageMetricsResponse{
		storeMetrics:     metrics,
		RateLimitedTotal: s.rateLimitedTotal.Load(),
		OverloadedTotal:  s.overloadedTotal.Load(),
		CapacityRejected: s.capacityRejected.Load(),
		ActiveCapacity:   s.store.capacity(),
		UptimeSeconds:    int64(time.Since(startedAt).Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *server) acknowledgeSecret(w http.ResponseWriter, r *http.Request) {
	s.updateClaim(w, r, true)
}

func (s *server) releaseSecret(w http.ResponseWriter, r *http.Request) {
	s.updateClaim(w, r, false)
}

func (s *server) updateClaim(w http.ResponseWriter, r *http.Request, acknowledge bool) {
	id := r.PathValue("id")
	if !validSecretID(id) {
		writeError(w, http.StatusNotFound, "secret unavailable")
		return
	}
	var input claimRequest
	if err := decodeJSON(w, r, 1024, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	if input.Token == "" {
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
		if errors.Is(err, errStorageFailure) {
			writeError(w, http.StatusServiceUnavailable, "storage unavailable")
			return
		}
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
		w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func integerEnvironment(name string, defaultValue, minimum, maximum int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, errors.New(name + " must be within its documented range")
	}
	return parsed, nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: time.Second}
		response, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			log.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			log.Fatalf("unexpected healthcheck status: %s", response.Status)
		}
		return
	}

	ttl := defaultTTL
	if value := os.Getenv("SECRET_TTL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < minimumTTL || parsed > maximumTTL {
			log.Fatal("SECRET_TTL must be between 1m and 24h")
		}
		ttl = parsed
	}
	createRatePerMinute, err := integerEnvironment("CREATE_RATE_PER_MINUTE", defaultCreateRatePerMinute, 1, 100_000)
	if err != nil {
		log.Fatal(err)
	}
	createRateBurst, err := integerEnvironment("CREATE_RATE_BURST", defaultCreateRateBurst, 1, 100_000)
	if err != nil {
		log.Fatal(err)
	}
	activeSecretLimit, err := integerEnvironment("ACTIVE_SECRET_LIMIT", maxActiveSecrets, 1, maxConfiguredActiveSecrets)
	if err != nil {
		log.Fatal(err)
	}
	trustedProxies, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		log.Fatal(err)
	}
	metricsToken := os.Getenv("METRICS_TOKEN")
	if metricsToken != "" && (len(metricsToken) < 32 || strings.ContainsAny(metricsToken, " \t\r\n")) {
		log.Fatal("METRICS_TOKEN must contain at least 32 characters and no whitespace")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	var store secretStorage
	var closeStore func() error
	switch os.Getenv("STORAGE_BACKEND") {
	case "", "memory":
		store = newSecretStoreWithLimit(ttl, activeSecretLimit)
	case "sqlite":
		if os.Getenv("SQLITE_NO_PAYLOAD_BACKUP_ACK") != "yes" {
			log.Fatal("SQLite requires SQLITE_NO_PAYLOAD_BACKUP_ACK=yes after excluding the database directory from backups and snapshots")
		}
		db, err := openSQLiteStore(os.Getenv("SQLITE_PATH"), ttl, activeSecretLimit)
		if err != nil {
			log.Fatal(err)
		}
		closeStore = db.close
		store = db
	default:
		log.Fatal("STORAGE_BACKEND must be memory or sqlite")
	}
	service := &server{
		store:             store,
		startedAt:         time.Now(),
		trustedProxies:    trustedProxies,
		createRateLimiter: newClientRateLimiter(createRatePerMinute, createRateBurst),
	}
	if metricsToken != "" {
		service.metricsEnabled = true
		service.metricsTokenHash = sha256.Sum256([]byte(metricsToken))
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           service.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		if closeStore != nil {
			_ = closeStore()
		}
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Print("server started")
	if err := serveUntilStopped(ctx, listener, srv, store.cleanupExpired, closeStore, 8*time.Second); err != nil {
		log.Print("server shutdown: ", err)
		os.Exit(1)
	}
}
