package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDeadlineQueueUpdatesAndRemovesWithoutStaleEntries(t *testing.T) {
	var q deadlineQueue
	now := time.Now()
	for i := 0; i < 100; i++ {
		q.add(fmt.Sprint(i), now.Add(time.Duration(i)*time.Second))
	}
	q.add("99", now.Add(-time.Second))
	if q.first().id != "99" || q.Len() != 100 || len(q.byID) != 100 {
		t.Fatal("indexed update duplicated an entry or broke ordering")
	}
	for i := 0; i < 100; i++ {
		q.remove(fmt.Sprint(i))
	}
	if q.first() != nil || q.Len() != 0 || len(q.byID) != 0 {
		t.Fatal("removed deadlines retained stale entries")
	}
}

func TestIndexedStoreExpiryAndRetentionAreIndependentOfMaintenance(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }
	p := validPayload()
	id, _, token, err := store.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, _, err := store.claim(id); !errors.Is(err, errSecretUnavailable) {
		t.Fatal("expired secret was disclosed without janitor")
	}
	if store.secrets[id].Ciphertext != "" || store.expirations.Len() != 0 || store.terminals.Len() != 1 {
		t.Fatal("expiry retained payload or stale index")
	}
	now = now.Add(trackingRetention)
	if _, err := store.status(id, token); !errors.Is(err, errSecretUnavailable) {
		t.Fatal("terminal status exceeded its retention without janitor")
	}
	store.cleanupExpired()
	if len(store.secrets) != 0 || store.terminals.Len() != 0 {
		t.Fatal("terminal cleanup retained an indexed record")
	}
}

func TestIndexedStoreLongTTLConsumptionDoesNotAccumulateDeadlines(t *testing.T) {
	store := newSecretStore(24 * time.Hour)
	p := validPayload()
	for i := 0; i < maxTrackedSecrets+100; i++ {
		id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := store.claim(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.acknowledge(id, token, validProof()); err != nil {
			t.Fatal(err)
		}
	}
	if store.expirations.Len() != 0 || len(store.activeIDs) != 0 || store.terminals.Len() != maxTrackedSecrets || len(store.secrets) != maxTrackedSecrets {
		t.Fatal("deadline/history indexes grew beyond live records")
	}
}

func TestIndexedMaintenanceHasBoundedBatchAndExactMetrics(t *testing.T) {
	store := newSecretStore(time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }
	p := validPayload()
	for i := 0; i < 200; i++ {
		if _, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Minute)
	store.cleanupExpired()
	if store.activeCount != 136 {
		t.Fatal("maintenance did not honor its 64-entry expiry budget")
	}
	if metrics := store.metrics(); metrics.Active != 0 || metrics.ExpiredTotal != 200 {
		t.Fatal("metrics failed to reconcile simultaneous active expiry")
	}
}
