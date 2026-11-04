// Package stats stores the 统计 tab's counters. Every protocol face writes one
// file, because the four plugin faces are four shared libraries with separate
// memory; the main face merges its own in-memory counters with the timing
// counters its siblings persisted.
//
// The store is deliberately small: token counters, a list-price cost in
// nano-USD, TTFT and generation nanoseconds, and hourly buckets for the current
// monthly window. No prompt, response, key, or session content is stored.
package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version is the on-disk schema version.
const Version = 1

// FlushInterval is how long the store accumulates before writing. The dashboard
// states this so a fresh request can be a few seconds late there.
const FlushInterval = 5 * time.Second

// Retention bounds how many hourly buckets are kept. The monthly window needs
// at most a month; the extra days absorb a quota reset that lands slightly
// earlier than a calendar month.
const Retention = 40 * 24 * time.Hour

const bucketLayout = "2006-01-02T15"

// Counter is the cumulative count for one model, one hour bucket, or one time
// range. All fields are additive.
type Counter struct {
	Requests            int64 `json:"requests,omitempty"`
	Failed              int64 `json:"failed_requests,omitempty"`
	Canceled            int64 `json:"canceled_requests,omitempty"`
	Streams             int64 `json:"stream_requests,omitempty"`
	Unmetered           int64 `json:"unmetered_requests,omitempty"`
	Priced              int64 `json:"priced_requests,omitempty"`
	Unpriced            int64 `json:"unpriced_requests,omitempty"`
	Input               int64 `json:"input_tokens,omitempty"`
	Output              int64 `json:"output_tokens,omitempty"`
	Reasoning           int64 `json:"reasoning_tokens,omitempty"`
	Cached              int64 `json:"cached_tokens,omitempty"`
	CacheRead           int64 `json:"cache_read_tokens,omitempty"`
	CacheCreation       int64 `json:"cache_creation_tokens,omitempty"`
	Total               int64 `json:"total_tokens,omitempty"`
	CostNanoUSD         int64 `json:"cost_nano_usd,omitempty"`
	StreamOutput        int64 `json:"stream_output_tokens,omitempty"`
	StreamOutputSamples int64 `json:"stream_output_samples,omitempty"`
	// LatencyOutput and LatencyNano are the paired numerator and denominator
	// for the end-to-end TPS: one successful request's effective output tokens
	// divided by the host-reported request latency. Both fields are updated
	// from the same usage record, so the TPS is never assembled from a token
	// set and a timing set that cover different requests.
	LatencyOutput  int64 `json:"latency_output_tokens,omitempty"`
	LatencyNano    int64 `json:"latency_nano,omitempty"`
	LatencySamples int64 `json:"latency_samples,omitempty"`
	// TTFTNano/TTFTSamples come from the plugin's own stream reader. The host
	// does not populate TTFT on this async stream bridge. Only streams with an
	// actual content frame contribute; there is deliberately no first-frame
	// fallback.
	TTFTNano    int64 `json:"ttft_nano,omitempty"`
	TTFTSamples int64 `json:"ttft_samples,omitempty"`
	// GenerationNano/GenSamples are retained for diagnostics only. They are not
	// used for TPS because a token count cannot be paired with these samples
	// without a request id.
	GenerationNano int64 `json:"generation_nano,omitempty"`
	GenSamples     int64 `json:"generation_samples,omitempty"`
}

// Add accumulates another counter.
func (c *Counter) Add(other Counter) {
	c.Requests += other.Requests
	c.Failed += other.Failed
	c.Canceled += other.Canceled
	c.Streams += other.Streams
	c.Unmetered += other.Unmetered
	c.Priced += other.Priced
	c.Unpriced += other.Unpriced
	c.Input += other.Input
	c.Output += other.Output
	c.Reasoning += other.Reasoning
	c.Cached += other.Cached
	c.CacheRead += other.CacheRead
	c.CacheCreation += other.CacheCreation
	c.Total += other.Total
	c.CostNanoUSD += other.CostNanoUSD
	c.StreamOutput += other.StreamOutput
	c.StreamOutputSamples += other.StreamOutputSamples
	c.LatencyOutput += other.LatencyOutput
	c.LatencyNano += other.LatencyNano
	c.LatencySamples += other.LatencySamples
	c.TTFTNano += other.TTFTNano
	c.TTFTSamples += other.TTFTSamples
	c.GenerationNano += other.GenerationNano
	c.GenSamples += other.GenSamples
}

// MergeTiming accumulates only the plugin-measured timing counters. A sibling
// face must never contribute its token counters: the main face receives every
// usage record, so merging sibling tokens would count a request twice.
func (c *Counter) MergeTiming(other Counter) {
	c.TTFTNano += other.TTFTNano
	c.TTFTSamples += other.TTFTSamples
	c.GenerationNano += other.GenerationNano
	c.GenSamples += other.GenSamples
}

// Empty reports whether the counter has recorded any events.
func (c Counter) Empty() bool {
	return c.Requests == 0 && c.Total == 0 && c.TTFTSamples == 0 && c.GenSamples == 0 &&
		c.LatencySamples == 0 && c.StreamOutputSamples == 0 && c.CostNanoUSD == 0 && c.Canceled == 0
}

func (c Counter) empty() bool {
	return c.Empty()
}

// UsageSample is one host usage record after filtering and model resolution.
type UsageSample struct {
	Model         string
	Family        string
	Key           string
	At            time.Time
	Input         int64
	Output        int64
	Reasoning     int64
	Cached        int64
	CacheRead     int64
	CacheCreation int64
	Total         int64
	CostNanoUSD   int64
	Priced        bool
	Stream        bool
	Failed        bool
	// Canceled marks a request the downstream client aborted before the
	// response completed. The upstream did not fail, so it is counted apart
	// from Failed and never enters the throughput samples.
	Canceled  bool
	Unmetered bool
	// Latency is the host-reported wall-clock duration of this request. It is
	// paired with TPSOutput below, so TPS is computed from one usage record.
	Latency time.Duration
	// TPSOutput is the effective output token count for throughput. For Gemini
	// the host reports reasoning separately from visible output, while the
	// latency covers both, so reasoning is added here. For the other families
	// the upstream output count already includes reasoning.
	TPSOutput int64
}

// TimingSample is one successfully completed stream as measured by the plugin
// itself, because the host does not receive TTFT for this plugin's async stream
// bridge.
type TimingSample struct {
	Model      string
	Family     string
	Key        string
	At         time.Time
	TTFT       time.Duration
	Generation time.Duration
}

// KeyStats stores counters aggregated for one specific key.
type KeyStats struct {
	FirstUsed time.Time `json:"first_used,omitempty"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Counter
	Hours map[string]Counter `json:"hours,omitempty"`
}

// ModelStats is the persisted state of one model.
type ModelStats struct {
	Family    string    `json:"family,omitempty"`
	FirstUsed time.Time `json:"first_used,omitempty"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Counter
	Hours map[string]Counter   `json:"hours,omitempty"`
	Keys  map[string]*KeyStats `json:"keys,omitempty"`
}

func (m *ModelStats) ensureKey(key string) *KeyStats {
	if m.Keys == nil {
		m.Keys = map[string]*KeyStats{}
	}
	ks := m.Keys[key]
	if ks == nil {
		ks = &KeyStats{Hours: map[string]Counter{}}
		m.Keys[key] = ks
	}
	if ks.Hours == nil {
		ks.Hours = map[string]Counter{}
	}
	return ks
}

func (m *ModelStats) addUsage(sample UsageSample, at time.Time) {
	counter := Counter{
		Requests:      1,
		Input:         sample.Input,
		Output:        sample.Output,
		Reasoning:     sample.Reasoning,
		Cached:        sample.Cached,
		CacheRead:     sample.CacheRead,
		CacheCreation: sample.CacheCreation,
		Total:         sample.Total,
	}
	if sample.Failed {
		counter.Failed = 1
	} else if sample.Canceled {
		counter.Canceled = 1
	}
	if sample.Stream {
		counter.Streams = 1
		if !sample.Failed && !sample.Canceled && sample.Output > 0 {
			counter.StreamOutput = sample.Output
			counter.StreamOutputSamples = 1
		}
	}
	if sample.Unmetered {
		counter.Unmetered = 1
	}
	if sample.Priced {
		counter.Priced = 1
		counter.CostNanoUSD = sample.CostNanoUSD
	} else {
		counter.Unpriced = 1
	}
	// TPS is intentionally end-to-end: output tokens / host-reported latency.
	// Both sides come from this same usage record. Failed and canceled requests
	// are excluded so a timeout or a client abort with partial output does not
	// depress the throughput estimate.
	if !sample.Failed && !sample.Canceled && sample.TPSOutput > 0 && sample.Latency > 0 {
		counter.LatencyOutput = sample.TPSOutput
		counter.LatencyNano = int64(sample.Latency)
		counter.LatencySamples = 1
	}
	m.Counter.Add(counter)
	bucket := at.UTC().Format(bucketLayout)
	hour := m.Hours[bucket]
	hour.Add(counter)
	m.Hours[bucket] = hour

	key := strings.TrimSpace(sample.Key)
	if key != "" {
		ks := m.ensureKey(key)
		if ks.FirstUsed.IsZero() || at.Before(ks.FirstUsed) {
			ks.FirstUsed = at
		}
		if ks.LastUsed.IsZero() || at.After(ks.LastUsed) {
			ks.LastUsed = at
		}
		ks.Counter.Add(counter)
		kh := ks.Hours[bucket]
		kh.Add(counter)
		ks.Hours[bucket] = kh
	}
}

func (m *ModelStats) addTiming(sample TimingSample, at time.Time) {
	counter := Counter{TTFTSamples: 1, TTFTNano: int64(sample.TTFT)}
	if sample.Generation > 0 {
		counter.GenSamples = 1
		counter.GenerationNano = int64(sample.Generation)
	}
	m.Counter.Add(counter)
	bucket := at.UTC().Format(bucketLayout)
	hour := m.Hours[bucket]
	hour.Add(counter)
	m.Hours[bucket] = hour

	key := strings.TrimSpace(sample.Key)
	if key != "" {
		ks := m.ensureKey(key)
		if ks.FirstUsed.IsZero() || at.Before(ks.FirstUsed) {
			ks.FirstUsed = at
		}
		if ks.LastUsed.IsZero() || at.After(ks.LastUsed) {
			ks.LastUsed = at
		}
		ks.Counter.Add(counter)
		kh := ks.Hours[bucket]
		kh.Add(counter)
		ks.Hours[bucket] = kh
	}
}

// File is the on-disk shape of one protocol face's counters.
type File struct {
	Version         int                    `json:"version"`
	Role            string                 `json:"role"`
	WrittenAt       time.Time              `json:"written_at,omitempty"`
	ReceivedRecords int64                  `json:"received_records,omitempty"`
	MatchedRecords  int64                  `json:"matched_records,omitempty"`
	Models          map[string]*ModelStats `json:"models"`
}

// ModelSnapshot is one model's counters over the requested range.
type ModelSnapshot struct {
	Model     string
	Family    string
	FirstUsed time.Time
	LastUsed  time.Time
	Counter   Counter
	Keys      map[string]KeySnapshot
}

// KeySnapshot is one key's counters for a model over the requested range.
type KeySnapshot struct {
	FirstUsed time.Time
	LastUsed  time.Time
	Counter   Counter
}

// Snapshot is a read of the store over a range. A zero since means all time.
type Snapshot struct {
	Models    map[string]ModelSnapshot
	Received  int64
	Matched   int64
	LastError string
}

// Store owns one protocol face's counters and its debounced writer.
type Store struct {
	mu       sync.Mutex
	path     string
	role     string
	data     File
	dirty    bool
	readOnly bool
	lastErr  string
	stopped  bool
	started  bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	now      func() time.Time
}

// Open loads the store for one role. A missing file is not an error. A file
// from another role or a newer schema yields an empty, read-only store so the
// plugin never overwrites data it does not understand; a corrupt file starts
// empty and is rewritten after the warning, because the file is only a local
// counter cache. The returned error is reported as a warning by the dashboard.
func Open(path, role string) (*Store, error) {
	store := &Store{
		path:   path,
		role:   role,
		data:   newFile(role),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		now:    time.Now,
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return store, nil
		}
		store.lastErr = "读取统计文件失败: " + errRead.Error()
		return store, errors.New(store.lastErr)
	}
	var file File
	if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
		store.lastErr = "解析统计文件失败: " + errUnmarshal.Error()
		return store, errors.New(store.lastErr)
	}
	if file.Version != Version {
		store.readOnly = true
		store.lastErr = fmt.Sprintf("统计文件版本 %d 与插件版本 %d 不符，已停止写入", file.Version, Version)
		return store, errors.New(store.lastErr)
	}
	if file.Role != "" && file.Role != role {
		store.readOnly = true
		store.lastErr = fmt.Sprintf("统计文件属于 %s 面，本面是 %s，已停止写入", file.Role, role)
		return store, errors.New(store.lastErr)
	}
	normalizeFile(&file, role)
	store.data = file
	return store, nil
}

// ReadFile loads a sibling face's file without starting a writer.
func ReadFile(path, expectedRole string) (File, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return File{}, errRead
	}
	var file File
	if errUnmarshal := json.Unmarshal(raw, &file); errUnmarshal != nil {
		return File{}, fmt.Errorf("解析统计文件失败: %w", errUnmarshal)
	}
	if file.Version != Version {
		return File{}, fmt.Errorf("统计文件版本 %d 与插件版本 %d 不符", file.Version, Version)
	}
	if file.Role != "" && file.Role != expectedRole {
		return File{}, fmt.Errorf("统计文件属于 %s 面，期望 %s", file.Role, expectedRole)
	}
	normalizeFile(&file, expectedRole)
	return file, nil
}

func newFile(role string) File {
	return File{Version: Version, Role: role, Models: map[string]*ModelStats{}}
}

func normalizeFile(file *File, role string) {
	if file.Role == "" {
		file.Role = role
	}
	if file.Models == nil {
		file.Models = map[string]*ModelStats{}
	}
	for id, model := range file.Models {
		if model == nil {
			delete(file.Models, id)
			continue
		}
		if model.Hours == nil {
			model.Hours = map[string]Counter{}
		}
		if model.Keys == nil {
			model.Keys = map[string]*KeyStats{}
		}
		for kid, ks := range model.Keys {
			if ks == nil {
				delete(model.Keys, kid)
				continue
			}
			if ks.Hours == nil {
				ks.Hours = map[string]Counter{}
			}
		}
	}
}

// NoteRecord records whether one host usage event belonged to this plugin. It
// is the diagnostic that makes a provider-label mismatch visible.
func (s *Store) NoteRecord(matched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.readOnly {
		return
	}
	s.data.ReceivedRecords++
	if matched {
		s.data.MatchedRecords++
	}
	s.dirty = true
}

// ObserveUsage accumulates one matched usage record.
func (s *Store) ObserveUsage(sample UsageSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.readOnly {
		return
	}
	modelID := strings.TrimSpace(sample.Model)
	if modelID == "" {
		return
	}
	at := sample.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	model := s.modelLocked(modelID)
	if model.Family == "" {
		model.Family = sample.Family
	}
	if model.FirstUsed.IsZero() || at.Before(model.FirstUsed) {
		model.FirstUsed = at
	}
	if model.LastUsed.IsZero() || at.After(model.LastUsed) {
		model.LastUsed = at
	}
	model.addUsage(sample, at)
	s.dirty = true
	s.ensureStartedLocked()
}

// ObserveTiming accumulates one plugin-measured successful stream.
func (s *Store) ObserveTiming(sample TimingSample) {
	if sample.TTFT <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.readOnly {
		return
	}
	modelID := strings.TrimSpace(sample.Model)
	if modelID == "" {
		return
	}
	at := sample.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	model := s.modelLocked(modelID)
	if model.Family == "" {
		model.Family = sample.Family
	}
	if model.FirstUsed.IsZero() || at.Before(model.FirstUsed) {
		model.FirstUsed = at
	}
	if model.LastUsed.IsZero() || at.After(model.LastUsed) {
		model.LastUsed = at
	}
	model.addTiming(sample, at)
	s.dirty = true
	s.ensureStartedLocked()
}

func (s *Store) modelLocked(id string) *ModelStats {
	if s.data.Models == nil {
		s.data.Models = map[string]*ModelStats{}
	}
	model := s.data.Models[id]
	if model == nil {
		model = &ModelStats{Hours: map[string]Counter{}}
		s.data.Models[id] = model
	}
	if model.Hours == nil {
		model.Hours = map[string]Counter{}
	}
	return model
}

// RangeCounter returns one model's counter for a range. A zero since means
// all time; otherwise only hourly buckets starting at or after the hour that
// contains since are summed. It is shared by the store and the main face's
// sibling-file merge so both use the same bucket rule.
func RangeCounter(model *ModelStats, since time.Time) Counter {
	if model == nil {
		return Counter{}
	}
	if since.IsZero() {
		return model.Counter
	}
	cutoff := since.UTC().Truncate(time.Hour)
	var out Counter
	for key, bucket := range model.Hours {
		at, errParse := time.ParseInLocation(bucketLayout, key, time.UTC)
		if errParse != nil || at.Before(cutoff) {
			continue
		}
		out.Add(bucket)
	}
	return out
}

// RangeKeyCounter returns one key's counter for a range.
func RangeKeyCounter(ks *KeyStats, since time.Time) Counter {
	if ks == nil {
		return Counter{}
	}
	if since.IsZero() {
		return ks.Counter
	}
	cutoff := since.UTC().Truncate(time.Hour)
	var out Counter
	for key, bucket := range ks.Hours {
		at, errParse := time.ParseInLocation(bucketLayout, key, time.UTC)
		if errParse != nil || at.Before(cutoff) {
			continue
		}
		out.Add(bucket)
	}
	return out
}

// Snapshot sums all-time totals or the hourly buckets at or after since.
func (s *Store) Snapshot(since time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		Models:    map[string]ModelSnapshot{},
		Received:  s.data.ReceivedRecords,
		Matched:   s.data.MatchedRecords,
		LastError: s.lastErr,
	}
	cutoff := since
	if !cutoff.IsZero() {
		cutoff = cutoff.UTC().Truncate(time.Hour)
	}
	for id, model := range s.data.Models {
		counter := RangeCounter(model, cutoff)
		if counter.empty() {
			continue
		}
		keysMap := make(map[string]KeySnapshot, len(model.Keys))
		for kName, ks := range model.Keys {
			kCounter := RangeKeyCounter(ks, cutoff)
			if !kCounter.empty() {
				keysMap[kName] = KeySnapshot{
					FirstUsed: ks.FirstUsed,
					LastUsed:  ks.LastUsed,
					Counter:   kCounter,
				}
			}
		}
		out.Models[id] = ModelSnapshot{
			Model:     id,
			Family:    model.Family,
			FirstUsed: model.FirstUsed,
			LastUsed:  model.LastUsed,
			Counter:   counter,
			Keys:      keysMap,
		}
	}
	return out
}

// LastError reports the most recent load or flush problem.
func (s *Store) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Flush writes the store if it has unsaved changes.
func (s *Store) Flush() { s.flush() }

func (s *Store) flush() {
	s.mu.Lock()
	if s.readOnly || !s.dirty {
		s.mu.Unlock()
		return
	}
	now := s.now().UTC()
	s.pruneLocked(now)
	s.data.WrittenAt = now
	raw, errMarshal := json.MarshalIndent(s.data, "", "  ")
	if errMarshal != nil {
		s.lastErr = "编码统计文件失败: " + errMarshal.Error()
		s.mu.Unlock()
		return
	}
	s.dirty = false
	path := s.path
	s.mu.Unlock()

	if errWrite := writeAtomic(path, raw); errWrite != nil {
		s.mu.Lock()
		s.dirty = true
		s.lastErr = errWrite.Error()
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.lastErr = ""
	s.mu.Unlock()
}

func (s *Store) pruneLocked(now time.Time) {
	cutoff := now.Add(-Retention).Truncate(time.Hour)
	for _, model := range s.data.Models {
		for key := range model.Hours {
			at, errParse := time.ParseInLocation(bucketLayout, key, time.UTC)
			if errParse != nil || at.Before(cutoff) {
				delete(model.Hours, key)
			}
		}
		for _, ks := range model.Keys {
			for key := range ks.Hours {
				at, errParse := time.ParseInLocation(bucketLayout, key, time.UTC)
				if errParse != nil || at.Before(cutoff) {
					delete(ks.Hours, key)
				}
			}
		}
	}
}

func (s *Store) ensureStartedLocked() {
	if s.started || s.stopped {
		return
	}
	s.started = true
	go s.loop()
}

func (s *Store) loop() {
	timer := time.NewTimer(FlushInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.stopCh:
			s.flush()
			close(s.doneCh)
			return
		case <-timer.C:
			s.flush()
			timer.Reset(FlushInterval)
		}
	}
}

// Shutdown stops the writer and flushes one last time. It is safe to call more
// than once.
func (s *Store) Shutdown() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	started := s.started
	s.mu.Unlock()
	if !started {
		s.flush()
		return
	}
	close(s.stopCh)
	<-s.doneCh
}

func writeAtomic(path string, raw []byte) error {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return fmt.Errorf("创建统计目录失败: %w", errMkdir)
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
		return fmt.Errorf("写入统计文件失败: %w", errWrite)
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		return fmt.Errorf("发布统计文件失败: %w", errRename)
	}
	return nil
}
