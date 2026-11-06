package main

import (
	"container/heap"
	"time"
)

type deadlineEntry struct {
	id    string
	at    time.Time
	index int
}

// Every ID has at most one indexed entry. Consuming/evicting removes it, so
// stale entries cannot accumulate with throughput or long TTLs.
type deadlineQueue struct {
	entries []*deadlineEntry
	byID    map[string]*deadlineEntry
}

func (q deadlineQueue) Len() int { return len(q.entries) }
func (q deadlineQueue) Less(i, j int) bool {
	return q.entries[i].at.Before(q.entries[j].at)
}
func (q deadlineQueue) Swap(i, j int) {
	q.entries[i], q.entries[j] = q.entries[j], q.entries[i]
	q.entries[i].index, q.entries[j].index = i, j
}
func (q *deadlineQueue) Push(value any) {
	entry := value.(*deadlineEntry)
	entry.index = len(q.entries)
	q.entries = append(q.entries, entry)
}
func (q *deadlineQueue) Pop() any {
	last := len(q.entries) - 1
	entry := q.entries[last]
	q.entries[last] = nil
	q.entries = q.entries[:last]
	delete(q.byID, entry.id)
	return entry
}
func (q *deadlineQueue) add(id string, at time.Time) {
	if q.byID == nil {
		q.byID = make(map[string]*deadlineEntry)
	}
	if entry, ok := q.byID[id]; ok {
		entry.at = at
		heap.Fix(q, entry.index)
		return
	}
	entry := &deadlineEntry{id: id, at: at}
	q.byID[id] = entry
	heap.Push(q, entry)
}
func (q *deadlineQueue) remove(id string) {
	if entry, ok := q.byID[id]; ok {
		heap.Remove(q, entry.index)
	}
}
func (q *deadlineQueue) first() *deadlineEntry {
	if len(q.entries) == 0 {
		return nil
	}
	return q.entries[0]
}
