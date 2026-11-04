package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T, role string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stats-"+role+".json")
	store, errOpen := Open(path, role)
	if errOpen != nil {
		t.Fatalf("Open(%s): %v", role, errOpen)
	}
	t.Cleanup(store.Shutdown)
	return store, path
}

func TestStoreObservesUsageAndTiming(t *testing.T) {
	store, path := openTemp(t, "chat")
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	store.NoteRecord(false)
	store.NoteRecord(true)
	store.ObserveUsage(UsageSample{
		Model:         "glm-5.3",
		Family:        "chat",
		Key:           "main-key",
		At:            at,
		Input:         1_000,
		Output:        200,
		Reasoning:     50,
		CacheRead:     400,
		CacheCreation: 10,
		Total:         1_660,
		CostNanoUSD:   12_345_000,
		Priced:        true,
		Stream:        true,
		Latency:       4 * time.Second,
		TPSOutput:     200,
	})
	store.ObserveTiming(TimingSample{
		Model:      "glm-5.3",
		Family:     "chat",
		Key:        "main-key",
		At:         at,
		TTFT:       800 * time.Millisecond,
		Generation: 2 * time.Second,
	})

	snapshot := store.Snapshot(time.Time{})
	model, ok := snapshot.Models["glm-5.3"]
	if !ok {
		t.Fatal("model missing from snapshot")
	}
	if len(model.Keys) != 1 {
		t.Fatalf("expected 1 key in snapshot, got %d", len(model.Keys))
	}
	keySnap, okKey := model.Keys["main-key"]
	if !okKey {
		t.Fatal("main-key missing from model snapshot keys")
	}
	if keySnap.Counter.Requests != 1 || keySnap.Counter.Input != 1000 || keySnap.Counter.TTFTNano == 0 {
		t.Errorf("keySnap counters = %+v", keySnap.Counter)
	}
	if snapshot.Received != 2 || snapshot.Matched != 1 {
		t.Errorf("received/matched = %d/%d, want 2/1", snapshot.Received, snapshot.Matched)
	}
	if model.Counter.Requests != 1 || model.Counter.Input != 1_000 || model.Counter.Output != 200 ||
		model.Counter.Reasoning != 50 || model.Counter.CacheRead != 400 || model.Counter.CacheCreation != 10 ||
		model.Counter.Total != 1_660 || model.Counter.CostNanoUSD != 12_345_000 {
		t.Errorf("usage counter = %+v", model.Counter)
	}
	if model.Counter.StreamOutput != 200 || model.Counter.TTFTSamples != 1 || model.Counter.GenSamples != 1 {
		t.Errorf("stream/timing counters = %+v", model.Counter)
	}
	if model.Counter.LatencyOutput != 200 || model.Counter.LatencyNano != int64(4*time.Second) || model.Counter.LatencySamples != 1 {
		t.Errorf("paired throughput counters = %+v", model.Counter)
	}
	if model.Counter.TTFTNano != int64(800*time.Millisecond) || model.Counter.GenerationNano != int64(2*time.Second) {
		t.Errorf("timing nanos = %d/%d", model.Counter.TTFTNano, model.Counter.GenerationNano)
	}
	if !model.FirstUsed.Equal(at) || !model.LastUsed.Equal(at) {
		t.Errorf("first/last used = %s/%s, want %s", model.FirstUsed, model.LastUsed, at)
	}

	// The flushed file keeps the same counters without any prompt or key.
	store.Flush()
	file, errRead := ReadFile(path, "chat")
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	persisted := file.Models["glm-5.3"]
	if persisted == nil || persisted.Counter.Requests != 1 || persisted.Counter.TTFTNano == 0 {
		t.Fatalf("persisted counters = %+v", persisted)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		t.Fatal("stats file is empty")
	}
}

func TestStoreRangeUsesHourBuckets(t *testing.T) {
	store, _ := openTemp(t, "chat")
	base := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	store.ObserveUsage(UsageSample{Model: "glm-5.3", At: base, Input: 10, Output: 1, Priced: true, Stream: true, Latency: time.Second, TPSOutput: 1})
	store.ObserveUsage(UsageSample{Model: "glm-5.3", At: base.Add(3 * time.Hour), Input: 20, Output: 2, Priced: true, Stream: true, Latency: 2 * time.Second, TPSOutput: 2})
	store.ObserveTiming(TimingSample{Model: "glm-5.3", At: base, TTFT: time.Second, Generation: time.Second})
	store.ObserveTiming(TimingSample{Model: "glm-5.3", At: base.Add(3 * time.Hour), TTFT: 2 * time.Second, Generation: 2 * time.Second})

	rangeStart := base.Add(3 * time.Hour)
	window := store.Snapshot(rangeStart)
	counter := window.Models["glm-5.3"].Counter
	if counter.Requests != 1 || counter.Input != 20 || counter.Output != 2 {
		t.Errorf("range counters = %+v, want only the later request", counter)
	}
	if counter.TTFTSamples != 1 || counter.TTFTNano != int64(2*time.Second) {
		t.Errorf("range timing = %+v, want only the later sample", counter)
	}

	all := store.Snapshot(time.Time{})
	allCounter := all.Models["glm-5.3"].Counter
	if allCounter.Requests != 2 || allCounter.Input != 30 || allCounter.TTFTSamples != 2 {
		t.Errorf("all-time counters = %+v", allCounter)
	}
}

func TestStorePrunesOldHourBucketsButKeepsTotals(t *testing.T) {
	store, path := openTemp(t, "chat")
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return start }
	store.ObserveUsage(UsageSample{Model: "glm-5.3", At: start, Input: 10, Priced: true})
	store.now = func() time.Time { return start.Add(41 * 24 * time.Hour) }
	store.Flush()

	file, errRead := ReadFile(path, "chat")
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	model := file.Models["glm-5.3"]
	if model == nil || model.Requests != 1 {
		t.Fatalf("all-time total must survive pruning: %+v", model)
	}
	if len(model.Hours) != 0 {
		t.Errorf("hour buckets after retention = %v, want none", model.Hours)
	}
	// The all-time snapshot still sees the request.
	if got := store.Snapshot(time.Time{}).Models["glm-5.3"].Counter.Requests; got != 1 {
		t.Errorf("all-time requests = %d", got)
	}
}

func TestStoreRoleMismatchIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats-chat.json")
	seed := File{Version: Version, Role: "messages", Models: map[string]*ModelStats{
		"claude-haiku-5-5": {Family: "messages", Hours: map[string]Counter{}},
	}}
	raw, _ := json.Marshal(seed)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store, errOpen := Open(path, "chat")
	if errOpen == nil {
		t.Fatal("a file from another role must be reported")
	}
	if !store.readOnly {
		t.Fatal("a file from another role must not be overwritten")
	}
	store.ObserveUsage(UsageSample{Model: "glm-5.3", Input: 1})
	store.Flush()
	file, errRead := ReadFile(path, "messages")
	if errRead != nil {
		t.Fatalf("the original file must stay readable: %v", errRead)
	}
	if file.Role != "messages" || len(file.Models) != 1 {
		t.Errorf("the original file was modified: %+v", file)
	}
	if _, errReadChat := ReadFile(path, "chat"); errReadChat == nil {
		t.Error("ReadFile must reject a role mismatch")
	}
}

func TestStoreRecoversFromCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats-chat.json")
	if errWrite := os.WriteFile(path, []byte("not json"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store, errOpen := Open(path, "chat")
	if errOpen == nil {
		t.Fatal("a corrupt file must be reported")
	}
	store.ObserveUsage(UsageSample{Model: "glm-5.3", Input: 10, Priced: true})
	store.Flush()
	if _, errRead := ReadFile(path, "chat"); errRead != nil {
		t.Fatalf("the store must recover by rewriting: %v", errRead)
	}
	store.Shutdown()
}

func TestStoreShutdownWithoutWritesDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	store, errOpen := Open(filepath.Join(dir, "stats-chat.json"), "chat")
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	done := make(chan struct{})
	go func() { store.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown blocked without a writer")
	}
	if _, errStat := os.Stat(filepath.Join(dir, "stats-chat.json")); !os.IsNotExist(errStat) {
		t.Error("a store with no samples must not create a file")
	}
}

func TestMergeTimingIgnoresSiblingTokens(t *testing.T) {
	main := Counter{Requests: 10, Input: 100, CostNanoUSD: 5}
	sibling := Counter{Requests: 99, Input: 999, CostNanoUSD: 999, TTFTNano: 5, TTFTSamples: 1, GenerationNano: 10, GenSamples: 1}
	main.MergeTiming(sibling)
	if main.Requests != 10 || main.Input != 100 || main.CostNanoUSD != 5 {
		t.Errorf("MergeTiming must not import token counters: %+v", main)
	}
	if main.TTFTSamples != 1 || main.GenSamples != 1 {
		t.Errorf("MergeTiming lost timing: %+v", main)
	}
}

// A canceled request is counted as a request and as canceled, never as failed,
// and it never contributes stream-output or throughput samples.
func TestStoreCountsCanceledApartFromFailed(t *testing.T) {
	store, path := openTemp(t, "chat")
	at := time.Date(2026, 11, 4, 2, 33, 0, 0, time.UTC)
	store.ObserveUsage(UsageSample{
		Model: "gemini-4-argon", Family: "gemini", Key: "main", At: at,
		Stream: true, Canceled: true, Priced: true,
		Input: 100, Output: 10, Total: 110,
		Latency: 4 * time.Second, TPSOutput: 10,
	})

	model, exists := store.Snapshot(time.Time{}).Models["gemini-4-argon"]
	if !exists {
		t.Fatal("canceled usage did not reach the store")
	}
	if model.Counter.Requests != 1 || model.Counter.Canceled != 1 || model.Counter.Failed != 0 {
		t.Errorf("requests/canceled/failed = %+v, want 1/1/0", model.Counter)
	}
	if model.Counter.StreamOutput != 0 || model.Counter.StreamOutputSamples != 0 ||
		model.Counter.LatencySamples != 0 || model.Counter.LatencyOutput != 0 {
		t.Errorf("a canceled request must not enter TPS/stream-output samples: %+v", model.Counter)
	}
	store.Flush()
	file, errRead := ReadFile(path, "chat")
	if errRead != nil {
		t.Fatalf("ReadFile: %v", errRead)
	}
	persisted := file.Models["gemini-4-argon"]
	if persisted == nil {
		t.Fatal("canceled usage was not persisted")
	}
	hour := persisted.Hours["2026-11-04T02"]
	if hour.Canceled != 1 || hour.Failed != 0 || hour.Requests != 1 {
		t.Errorf("hour bucket = %+v, want one canceled request", hour)
	}

	var merged Counter
	merged.Add(model.Counter)
	merged.Add(model.Counter)
	if merged.Canceled != 2 || merged.Failed != 0 {
		t.Errorf("merged counter = %+v, want canceled=2 failed=0", merged)
	}
	if merged.Empty() {
		t.Error("a counter with canceled requests is not empty")
	}
}
