package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Pheobe-Southwood/cpa-opencodego/internal/roles"
	"github.com/Pheobe-Southwood/cpa-opencodego/internal/stats"
)

func TestOpenCodeProviderFilter(t *testing.T) {
	for _, provider := range []string{
		"openai-compatible-opencode go",
		"cpa-opencodego",
		"cpa-opencodego-gemini",
		"cpa-opencodego-responses",
		"cpa-opencodego-messages",
	} {
		if !isOpenCodeProvider(provider) {
			t.Errorf("%q must be imported", provider)
		}
	}
	for _, provider := range []string{"antigravity", "openai-compatible-tierflow", ""} {
		if isOpenCodeProvider(provider) {
			t.Errorf("%q must be ignored", provider)
		}
	}
}

func TestCanonicalModelUsesAlias(t *testing.T) {
	aliases := map[string]string{
		"deepseek-v4.1-flash":       "deepseek-v4.1-flash",
		"deepseek-v4.1-flash-alias": "deepseek-v4.1-flash",
	}
	if got := canonicalModel("DeepSeek-V4.1-Flash", "", map[string]string{
		"deepseek-v4.1-flash": "deepseek-v4.1-flash",
	}); got != "deepseek-v4.1-flash" {
		t.Errorf("case-insensitive model = %q", got)
	}
	if got := canonicalModel("", "DeepSeek-V4.1-Flash", aliases); got != "deepseek-v4.1-flash" {
		t.Errorf("alias fallback = %q", got)
	}
	if got := canonicalModel("brand-new", "", aliases); got != "brand-new" {
		t.Errorf("unknown model = %q", got)
	}
}

func TestTrackerRecordCarriesPairedTPSAndTiming(t *testing.T) {
	record, ok := trackerRecord(trackerRequest{
		Time:         time.Date(2026, 10, 5, 5, 30, 0, 0, time.UTC),
		Provider:     "openai-compatible-opencode go",
		Model:        "DeepSeek-V4.1-Flash",
		OutputTokens: 100,
		InputTokens:  1_000,
		TotalTokens:  1_100,
		LatencyNS:    uint64(4 * time.Second),
		TTFTNS:       uint64(time.Second),
		GenerationNS: uint64(3 * time.Second),
	}, map[string]string{"deepseek-v4.1-flash": "deepseek-v4.1-flash"}, map[string]string{"deepseek-v4.1-flash": "chat"})
	if !ok {
		t.Fatal("record was rejected")
	}
	if record.sample.Stream != true || record.sample.Latency != 4*time.Second || record.sample.TPSOutput != 100 {
		t.Errorf("sample = %+v", record.sample)
	}
	if record.timing == nil || record.timing.TTFT != time.Second || record.timing.Generation != 3*time.Second {
		t.Errorf("timing = %+v", record.timing)
	}
}

func TestTrackerRecordGeminiReasoningIsTPSOutput(t *testing.T) {
	record, ok := trackerRecord(trackerRequest{
		Time:            time.Date(2026, 10, 5, 5, 30, 0, 0, time.UTC),
		Provider:        "cpa-opencodego-gemini",
		Model:           "gemini-4-argon",
		OutputTokens:    20,
		ReasoningTokens: 30,
		LatencyNS:       uint64(2 * time.Second),
	}, nil, nil)
	if !ok {
		t.Fatal("record was rejected")
	}
	if record.sample.Family != string(roles.Gemini) || record.sample.TPSOutput != 50 {
		t.Errorf("gemini sample = %+v", record.sample)
	}
}

func TestReadTrackerFiltersByProviderAndCutoff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tracker.db")
	db, errOpen := bolt.Open(path, 0o600, nil)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	if errCreate := db.Update(func(tx *bolt.Tx) error {
		bucket, errBucket := tx.CreateBucket([]byte(requestsBucket))
		if errBucket != nil {
			return errBucket
		}
		put := func(at time.Time, seq uint64, record trackerRequest) error {
			record.Time = at
			record.Sequence = seq
			raw, _ := json.Marshal(record)
			key := make([]byte, 16)
			binary.BigEndian.PutUint64(key[:8], uint64(at.UnixNano()))
			binary.BigEndian.PutUint64(key[8:], seq)
			return bucket.Put(key, raw)
		}
		base := time.Date(2026, 10, 5, 5, 30, 0, 0, time.UTC)
		if errPut := put(base, 1, trackerRequest{
			Provider: "openai-compatible-opencode go", Model: "DeepSeek-V4.1-Flash",
			OutputTokens: 10, InputTokens: 20, TotalTokens: 30,
			LatencyNS: uint64(2 * time.Second), TTFTNS: uint64(time.Second),
		}); errPut != nil {
			return errPut
		}
		if errPut := put(base.Add(time.Hour), 2, trackerRequest{
			Provider: "openai-compatible-opencode go", Model: "DeepSeek-V4.1-Flash",
			OutputTokens: 10, InputTokens: 20, TotalTokens: 30,
		}); errPut != nil {
			return errPut
		}
		return put(base.Add(2*time.Hour), 3, trackerRequest{
			Provider: "antigravity", Model: "gemini-3.8-flash-high",
			OutputTokens: 10, InputTokens: 20, TotalTokens: 30,
		})
	}); errCreate != nil {
		t.Fatal(errCreate)
	}
	_ = db.Close()

	cutoff := time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)
	records, summary, errRead := readTracker(path, cutoff, map[string]string{
		"deepseek-v4.1-flash": "deepseek-v4.1-flash",
	}, nil)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if len(records) != 1 || summary.scanned != 2 || summary.cutoffSkipped != 1 {
		t.Fatalf("records/summary = %d/%+v", len(records), summary)
	}
	if records[0].sample.Total != 30 || records[0].sample.Latency != 2*time.Second {
		t.Errorf("record = %+v", records[0].sample)
	}
}

func TestRunImportsOnceAndWritesMarker(t *testing.T) {
	dir := t.TempDir()
	statsDir := filepath.Join(dir, "stats")
	if errMkdir := os.MkdirAll(statsDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	settings := map[string]any{
		"settings": map[string]any{
			"models": map[string]any{
				"deepseek-v4.1-flash": map[string]any{"aliases": []string{"DeepSeek-V4.1-Flash"}},
			},
		},
	}
	rawSettings, _ := json.Marshal(settings)
	if errWrite := os.WriteFile(filepath.Join(statsDir, "shared.json"), rawSettings, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	dbPath := filepath.Join(dir, "tracker.db")
	db, errOpen := bolt.Open(dbPath, 0o600, nil)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	if errCreate := db.Update(func(tx *bolt.Tx) error {
		bucket, _ := tx.CreateBucket([]byte(requestsBucket))
		at := time.Date(2026, 10, 5, 5, 30, 0, 0, time.UTC)
		record := trackerRequest{
			Time: at, Sequence: 1, Provider: "openai-compatible-opencode go",
			Model: "DeepSeek-V4.1-Flash", InputTokens: 1_000, OutputTokens: 100, TotalTokens: 1_100,
			LatencyNS: uint64(2 * time.Second), TTFTNS: uint64(time.Second), GenerationNS: uint64(time.Second),
		}
		raw, _ := json.Marshal(record)
		key := make([]byte, 16)
		binary.BigEndian.PutUint64(key[:8], uint64(at.UnixNano()))
		binary.BigEndian.PutUint64(key[8:], 1)
		return bucket.Put(key, raw)
	}); errCreate != nil {
		t.Fatal(errCreate)
	}
	_ = db.Close()

	opt := options{
		trackerPath:  dbPath,
		statsDir:     statsDir,
		settingsPath: filepath.Join(statsDir, "shared.json"),
		apply:        true,
	}
	if errRun := run(opt); errRun != nil {
		t.Fatal(errRun)
	}
	file, errRead := stats.ReadFile(filepath.Join(statsDir, statsChatFile), statsChatRole)
	if errRead != nil {
		t.Fatal(errRead)
	}
	model := file.Models["deepseek-v4.1-flash"]
	if model == nil || model.Counter.Requests != 1 || model.Counter.LatencyOutput != 100 ||
		model.Counter.LatencyNano != int64(2*time.Second) || model.Counter.TTFTSamples != 1 {
		t.Fatalf("imported counters = %+v", model)
	}
	if _, errStat := os.Stat(filepath.Join(statsDir, markerFileName)); errStat != nil {
		t.Fatalf("marker missing: %v", errStat)
	}
	if errRun := run(opt); errRun == nil || !strings.Contains(errRun.Error(), "already exists") {
		t.Fatalf("second run error = %v, want marker refusal", errRun)
	}
}
