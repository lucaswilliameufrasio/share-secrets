package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteSchemaVersion = 20261030054312

type sqliteStore struct {
	db    *sql.DB
	batch *sqliteBatcher
	ttl   time.Duration
	limit int
	now   func() time.Time
}

func openSQLiteStore(path string, ttl time.Duration, limit int) (*sqliteStore, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("SQLITE_PATH must be a clean absolute path")
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(1000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "secure_delete(1)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &sqliteStore{db: db, ttl: ttl, limit: limit, now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var version int64
	var applied bool
	if err := db.QueryRowContext(ctx, "SELECT version_id,is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1").Scan(&version, &applied); err != nil || !applied || version != sqliteSchemaVersion {
		_ = db.Close()
		return nil, errors.New("SQLite schema missing or incompatible; run the pinned Goose migration explicitly")
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		_ = db.Close()
		return nil, errors.New("SQLite WAL mode unavailable")
	}
	var secure int
	if err := db.QueryRowContext(ctx, "PRAGMA secure_delete").Scan(&secure); err != nil || secure != 1 {
		_ = db.Close()
		return nil, errors.New("SQLite secure_delete unavailable")
	}
	s.batch = newSQLiteBatcher(db)
	return s, nil
}

func (s *sqliteStore) close() error {
	if s.batch != nil {
		s.batch.close()
	}
	return s.db.Close()
}

func (s *sqliteStore) defaultTTL() time.Duration { return s.ttl }
func (s *sqliteStore) capacity() int             { return s.limit }

func storageError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", errStorageFailure, err)
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (s *sqliteStore) transaction(fn func(context.Context, *sql.Tx) error) error {
	return s.batch.submit(fn)
}

func (s *sqliteStore) expire(ctx context.Context, tx *sql.Tx, now int64, id string, limit int) error {
	var rows *sql.Rows
	var err error
	if id != "" {
		rows, err = tx.QueryContext(ctx, "SELECT id FROM secrets WHERE status='active' AND expires_at<=? AND id=?", now, id)
	} else {
		rows, err = tx.QueryContext(ctx, "SELECT id FROM secrets WHERE status='active' AND expires_at<=? ORDER BY expires_at LIMIT ?", now, limit)
	}
	if err != nil {
		return storageError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return storageError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return storageError(err)
	}
	if err := rows.Close(); err != nil {
		return storageError(err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM payloads WHERE id=?", id); err != nil {
			return storageError(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE secrets SET status='expired',terminal_at=expires_at,claim_hash=NULL,claim_until=NULL WHERE id=?", id); err != nil {
			return storageError(err)
		}
	}
	if len(ids) > 0 {
		_, err := tx.ExecContext(ctx, "UPDATE store_counters SET expired=expired+?,purge_generation=purge_generation+1 WHERE singleton=1", len(ids))
		return storageError(err)
	}
	return nil
}

func (s *sqliteStore) createWithTTL(ciphertext, iv, proofHash string, ttl time.Duration) (string, time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(ciphertext)
	ivBytes, ivErr := base64.RawURLEncoding.DecodeString(iv)
	proof, proofErr := base64.RawURLEncoding.DecodeString(proofHash)
	if err != nil || len(decoded) < 16 || ivErr != nil || len(ivBytes) != 12 || proofErr != nil || len(proof) != 32 || ttl < minimumTTL || ttl > maximumTTL {
		return "", time.Time{}, "", errors.New("invalid encrypted payload")
	}
	id, err := randomToken(24)
	if err != nil {
		return "", time.Time{}, "", storageError(err)
	}
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, "", storageError(err)
	}
	hash := sha256.Sum256([]byte(token))
	var expires time.Time
	err = s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		expires = now.Add(ttl)
		if err := s.expire(ctx, tx, now.UnixNano(), "", 64); err != nil {
			return err
		}
		var active, total int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM secrets WHERE status='active'").Scan(&active); err != nil {
			return storageError(err)
		}
		if active >= s.limit {
			return errSecretStoreFull
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM secrets").Scan(&total); err != nil {
			return storageError(err)
		}
		if total >= maxTrackedSecrets {
			if _, err := tx.ExecContext(ctx, "DELETE FROM secrets WHERE id=(SELECT id FROM secrets WHERE status<>'active' ORDER BY terminal_at LIMIT 1)"); err != nil {
				return storageError(err)
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO secrets(id,created_at,expires_at,status,tracking_hash) VALUES(?,?,?,'active',?)", id, now.UnixNano(), expires.UnixNano(), hash[:]); err != nil {
			return storageError(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO payloads(id,ciphertext,iv,proof_hash) VALUES(?,?,?,?)", id, ciphertext, iv, proof); err != nil {
			return storageError(err)
		}
		_, err := tx.ExecContext(ctx, "UPDATE store_counters SET created=created+1 WHERE singleton=1")
		return storageError(err)
	})
	return id, expires, token, err
}

func (s *sqliteStore) claim(id string) (encryptedSecret, string, error) {
	token, err := randomToken(24)
	if err != nil {
		return encryptedSecret{}, "", storageError(err)
	}
	hash := sha256.Sum256([]byte(token))
	var item encryptedSecret
	var outcome error
	err = s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		if err := s.expire(ctx, tx, now.UnixNano(), id, 0); err != nil {
			return err
		}
		var until sql.NullInt64
		var expires int64
		err := tx.QueryRowContext(ctx, "SELECT p.ciphertext,p.iv,s.expires_at,s.claim_until FROM secrets s JOIN payloads p ON p.id=s.id WHERE s.id=? AND s.status='active'", id).Scan(&item.Ciphertext, &item.IV, &expires, &until)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = errSecretUnavailable
			return nil
		}
		if err != nil {
			return storageError(err)
		}
		if until.Valid && until.Int64 > now.UnixNano() {
			outcome = errSecretClaimed
			return nil
		}
		item.ExpiresAt = time.Unix(0, expires)
		if _, err := tx.ExecContext(ctx, "UPDATE secrets SET claim_hash=?,claim_until=?,claim_count=claim_count+1 WHERE id=?", hash[:], now.Add(claimLease).UnixNano(), id); err != nil {
			return storageError(err)
		}
		_, err = tx.ExecContext(ctx, "UPDATE store_counters SET claims=claims+1 WHERE singleton=1")
		return storageError(err)
	})
	if err != nil {
		return encryptedSecret{}, "", err
	}
	return item, token, outcome
}

func (s *sqliteStore) acknowledge(id, token, proof string) error {
	return s.updateClaim(id, token, proof, true)
}

func (s *sqliteStore) release(id, token string) error {
	return s.updateClaim(id, token, "", false)
}

func (s *sqliteStore) updateClaim(id, token, proof string, ack bool) error {
	var outcome error
	err := s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		if err := s.expire(ctx, tx, now.UnixNano(), id, 0); err != nil {
			return err
		}
		var claimHash, proofHash []byte
		var until sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT s.claim_hash,s.claim_until,p.proof_hash FROM secrets s JOIN payloads p ON p.id=s.id WHERE s.id=? AND s.status='active'", id).Scan(&claimHash, &until, &proofHash)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = errSecretUnavailable
			return nil
		}
		if err != nil {
			return storageError(err)
		}
		hash := sha256.Sum256([]byte(token))
		if token == "" || !until.Valid || until.Int64 <= now.UnixNano() || subtle.ConstantTimeCompare(hash[:], claimHash) != 1 {
			outcome = errInvalidClaim
			return nil
		}
		if !ack {
			_, err := tx.ExecContext(ctx, "UPDATE secrets SET claim_hash=NULL,claim_until=NULL WHERE id=?", id)
			return storageError(err)
		}
		proofBytes, err := base64.RawURLEncoding.DecodeString(proof)
		actual := sha256.Sum256(proofBytes)
		if err != nil || len(proofBytes) != 32 || subtle.ConstantTimeCompare(actual[:], proofHash) != 1 {
			outcome = errInvalidProof
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM payloads WHERE id=?", id); err != nil {
			return storageError(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE secrets SET status='used',used_at=?,terminal_at=?,claim_hash=NULL,claim_until=NULL WHERE id=?", now.UnixNano(), now.UnixNano(), id); err != nil {
			return storageError(err)
		}
		_, err = tx.ExecContext(ctx, "UPDATE store_counters SET consumed=consumed+1,purge_generation=purge_generation+1 WHERE singleton=1")
		return storageError(err)
	})
	if err != nil {
		return err
	}
	return outcome
}

func (s *sqliteStore) status(id, token string) (trackingStatus, error) {
	var result trackingStatus
	var outcome error
	err := s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		if err := s.expire(ctx, tx, now.UnixNano(), id, 0); err != nil {
			return err
		}
		var created, expires int64
		var used, terminal, until sql.NullInt64
		var hash []byte
		err := tx.QueryRowContext(ctx, "SELECT status,created_at,expires_at,used_at,terminal_at,claim_until,claim_count,tracking_hash FROM secrets WHERE id=?", id).Scan(&result.Status, &created, &expires, &used, &terminal, &until, &result.ClaimCount, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			outcome = errSecretUnavailable
			return nil
		}
		if err != nil {
			return storageError(err)
		}
		actual := sha256.Sum256([]byte(token))
		if token == "" || subtle.ConstantTimeCompare(actual[:], hash) != 1 || (terminal.Valid && terminal.Int64 <= now.Add(-trackingRetention).UnixNano()) {
			outcome = errSecretUnavailable
			return nil
		}
		result.ID = id
		result.CreatedAt = time.Unix(0, created).UTC()
		result.ExpiresAt = time.Unix(0, expires).UTC()
		if result.Status == "active" && until.Valid && until.Int64 > now.UnixNano() {
			result.Status = "opening"
		}
		if used.Valid {
			t := time.Unix(0, used.Int64).UTC()
			result.UsedAt = &t
		}
		return nil
	})
	if err != nil {
		return trackingStatus{}, err
	}
	return result, outcome
}

func (s *sqliteStore) readMetrics() (storeMetrics, error) {
	var result storeMetrics
	err := s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		now := s.now()
		if err := s.expire(ctx, tx, now.UnixNano(), "", 700); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT created,claims,consumed,expired,purge_generation>checkpoint_generation FROM store_counters WHERE singleton=1").Scan(&result.CreatedTotal, &result.ClaimsTotal, &result.ConsumedTotal, &result.ExpiredTotal, &result.CleanupPending); err != nil {
			return storageError(err)
		}
		return storageError(tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(status='active'),0),COALESCE(sum(status='active' AND claim_until>?),0) FROM secrets", now.UnixNano()).Scan(&result.Tracked, &result.Active, &result.Opening))
	})
	result.QueueDepth = s.batch.depth()
	result.QueueCapacity = sqliteQueueCapacity
	result.QueueRejectedTotal = s.batch.rejected.Load()
	result.BatchBeginFailures = s.batch.beginFailures.Load()
	result.BatchOperationFailures = s.batch.operationFailures.Load()
	result.BatchSavepointFailures = s.batch.savepointFailures.Load()
	result.BatchCommitFailures = s.batch.commitFailures.Load()
	result.QueueExpiredTotal = s.batch.expired.Load()
	result.BatchBusyErrors = s.batch.busyErrors.Load()
	result.BatchIOErrors = s.batch.ioErrors.Load()
	result.BatchLastIOCode = s.batch.lastIOCode.Load()
	result.BatchInterruptErrors = s.batch.interruptErrors.Load()
	result.BatchConstraintErrors = s.batch.constraintErrors.Load()
	result.BatchOtherErrors = s.batch.otherErrors.Load()
	return result, err
}

func (s *sqliteStore) cleanupExpired() {
	now := s.now()
	err := s.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if err := s.expire(ctx, tx, now.UnixNano(), "", 64); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM secrets WHERE id IN (SELECT id FROM secrets WHERE status<>'active' AND terminal_at<=? ORDER BY terminal_at LIMIT 64)", now.Add(-trackingRetention).UnixNano())
		return storageError(err)
	})
	if err != nil {
		log.Print("SQLite maintenance failed; will retry")
		return
	}
	if err := s.checkpoint(); err != nil {
		log.Print("SQLite WAL cleanup pending; will retry")
	}
}

func (s *sqliteStore) checkpoint() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var generation, completed int64
	if err := s.db.QueryRowContext(ctx, "SELECT purge_generation,checkpoint_generation FROM store_counters WHERE singleton=1").Scan(&generation, &completed); err != nil {
		return storageError(err)
	}
	if generation <= completed {
		return nil
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return storageError(err)
	}
	if busy != 0 {
		return errors.New("SQLite checkpoint busy")
	}
	_, err := s.db.ExecContext(ctx, "UPDATE store_counters SET checkpoint_generation=max(checkpoint_generation,?) WHERE singleton=1", generation)
	return storageError(err)
}
