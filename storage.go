package main

import "time"

// The lifecycle contract is intentionally narrow; PostgreSQL is not yet an
// implementation. Errors must propagate without turning storage failures into
// unavailable links or accidental successful acknowledgements.
type secretStorage interface {
	createWithTTL(string, string, string, time.Duration) (string, time.Time, string, error)
	claim(string) (encryptedSecret, string, error)
	acknowledge(string, string, string) error
	release(string, string) error
	status(string, string) (trackingStatus, error)
	readMetrics() (storeMetrics, error)
	cleanupExpired()
	defaultTTL() time.Duration
	capacity() int
}

func (s *secretStore) defaultTTL() time.Duration          { return s.ttl }
func (s *secretStore) capacity() int                      { return s.activeLimit }
func (s *secretStore) readMetrics() (storeMetrics, error) { return s.metrics(), nil }
