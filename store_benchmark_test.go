package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// A single store is shared by every worker. Seeding is outside the timer;
// measured iterations include real cleanup and terminal-metadata eviction.
func BenchmarkSharedStoreLifecycle(b *testing.B) {
	for _, retained := range []int{0, 1000, 9500} {
		b.Run(fmt.Sprintf("retained_%d", retained), func(b *testing.B) {
			store := newSecretStore(time.Hour)
			p := validPayload()
			for i := 0; i < retained; i++ {
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
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
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
		})
	}
}

func BenchmarkSharedStoreStatus(b *testing.B) {
	for _, active := range []int{1, 250, 499} {
		b.Run(fmt.Sprintf("active_%d", active), func(b *testing.B) {
			store := newSecretStore(time.Hour)
			p := validPayload()
			var id, token string
			for i := 0; i < active; i++ {
				var err error
				id, _, token, err = store.createWithTTL(p.Ciphertext, p.IV, p.ProofHash, time.Hour)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := store.status(id, token); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

// Exercise shared invariants in the ordinary race gate, not only benchmarks.
func TestSharedStoreLifecycleUnderContention(t *testing.T) {
	store := newSecretStore(time.Hour)
	p := validPayload()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Go(func() {
			for j := 0; j < 20; j++ {
				id, _, err := store.create(p.Ciphertext, p.IV, p.ProofHash)
				if err != nil {
					t.Error(err)
					return
				}
				_, token, err := store.claim(id)
				if err != nil {
					t.Error(err)
					return
				}
				if err := store.acknowledge(id, token, validProof()); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
	metrics := store.metrics()
	if metrics.Active != 0 || metrics.ConsumedTotal != 320 {
		t.Fatalf("contention invariant: active=%d consumed=%d", metrics.Active, metrics.ConsumedTotal)
	}
}
