// Package reqlog stores the 请求日志 tab's per-request records.
//
// The statistics package aggregates the host's usage events; this package is
// independent and records one row per upstream attempt from the plugin's own
// point of view, including the timings and token counters it measures while it
// proxies the response. That is what makes per-request generation TPS and TTFT
// possible: the host's usage event has no request id that could be joined to the
// plugin's stream timings.
//
// Every protocol face writes its own file because the four plugin builds are
// separate shared libraries; the main face merges the files for the dashboard.
// No prompt, response body, key secret, or raw session id is ever stored.
package reqlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version is the on-disk record version. A newer file is read but never
// rewritten, mirroring the statistics store's forward-compatibility rule.
const Version = 1

// MaxEntries bounds how many requests one protocol face keeps. 2000 rows cover
// a busy agent session without letting the file grow without bound.
const MaxEntries = 2000

// Retention drops records older than a week on load and compaction.
const Retention = 7 * 24 * time.Hour

// maxFileBytes triggers a compaction even when the row count is still small, so
// unusually long error messages cannot grow the file indefinitely.
const maxFileBytes = 4 << 20

// Entry is one upstream attempt, as measured by the plugin.
type Entry struct {
	// ID is unique within one protocol face.
	ID string `json:"id"`
	// At is when the attempt started.
	At time.Time `json:"at"`
	// Role is the protocol family that executed the request.
	Role string `json:"role"`
	// Model is the configured model id the dashboard groups by.
	Model string `json:"model"`
	// Requested is the client-facing id (an alias when one is in effect).
	Requested string `json:"requested,omitempty"`
	// UpstreamModel is the id sent to OpenCode Go.
	UpstreamModel string `json:"upstream_model,omitempty"`
	// Key is the configured key name; never the secret.
	Key string `json:"key,omitempty"`
	// Stream reports whether the request was executed in streaming mode.
	Stream bool `json:"stream"`
	// Status is the upstream HTTP status, zero when no response was received.
	Status int `json:"status,omitempty"`
	// Error is the failure summary, already truncated.
	Error string `json:"error,omitempty"`
	// LogID is the upstream x-opencode-log-id, useful when reporting an issue.
	LogID string `json:"log_id,omitempty"`
	// Session is a short hash of the x-opencode-session value, never the value.
	Session string `json:"session,omitempty"`
	// TTFTms is the time to the first content frame; zero when not measurable.
	TTFTms float64 `json:"ttft_ms,omitempty"`
	// Generationms is first content frame to last content frame.
	Generationms float64 `json:"generation_ms,omitempty"`
	// Totalms is the wall-clock time the attempt took.
	Totalms float64 `json:"total_ms"`
	// Token counters parsed from the upstream response.
	Input         int64 `json:"input_tokens,omitempty"`
	Output        int64 `json:"output_tokens,omitempty"`
	Reasoning     int64 `json:"reasoning_tokens,omitempty"`
	CacheRead     int64 `json:"cache_read_tokens,omitempty"`
	CacheCreation int64 `json:"cache_creation_tokens,omitempty"`
	TotalTokens   int64 `json:"total_tokens,omitempty"`
	// TokensSeen reports whether the upstream supplied a usage block. A failed
	// or usage-less request leaves it false so the dashboard shows "—" instead
	// of a misleading zero.
	TokensSeen bool `json:"tokens_seen,omitempty"`
	// Repairs lists compatibility fixes applied to this request (e.g. "developer-role", "reasoning-replay").
	Repairs []string `json:"repairs,omitempty"`
	// Finish is the completion reason ("stop", "length", "tool_calls", "eof", "canceled", "error").
	Finish string `json:"finish,omitempty"`
	// Incomplete reports whether an SSE stream truncated before carrying any finish_reason marker.
	Incomplete bool `json:"incomplete,omitempty"`
	// Canceled reports that the downstream client (or the host shutting the
	// stream bridge down) ended the request before the response completed. The
	// upstream itself was healthy, so a canceled attempt is not a failure.
	Canceled bool `json:"canceled,omitempty"`
	// Version is the record schema version, written for forward compatibility.
	Version int `json:"version,omitempty"`
}

// Failed reports whether the attempt failed. A downstream cancellation is not a
// failure: the upstream produced a normal response and only the client left.
func (e Entry) Failed() bool {
	if e.Canceled {
		return false
	}
	return e.Status >= 400 || strings.TrimSpace(e.Error) != ""
}

// Failed returns true when the entry is older than the retention window.
func (e Entry) Expired(now time.Time) bool {
	return !e.At.IsZero() && e.At.Before(now.Add(-Retention))
}

// Store owns one protocol face's records and its append-only file.
type Store struct {
	mu      sync.Mutex
	path    string
	role    string
	file    *os.File
	entries []Entry
	writes  int
	lastErr string
	stopped bool
}

// Open loads one face's records. A missing file is not an error. Corrupt lines
// are skipped with a warning because the file is a local diagnostic cache, not
// a system of record.
func Open(path, role string) (*Store, error) {
	store := &Store{path: path, role: role, entries: make([]Entry, 0, MaxEntries)}
	raw, errRead := os.ReadFile(path)
	var loadErr error
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			store.lastErr = "读取请求日志失败: " + errRead.Error()
			loadErr = errors.New(store.lastErr)
		}
	} else {
		entries, skipped := parseLines(raw, role)
		now := time.Now()
		kept := make([]Entry, 0, len(entries))
		for _, entry := range entries {
			if entry.Expired(now) {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) > MaxEntries {
			kept = kept[len(kept)-MaxEntries:]
		}
		store.entries = kept
		if skipped > 0 {
			store.lastErr = fmt.Sprintf("请求日志有 %d 行损坏记录，已跳过", skipped)
		}
		if len(kept) != len(entries) || skipped > 0 {
			if errCompact := store.compactLocked(); errCompact != nil {
				store.lastErr = "压缩请求日志失败: " + errCompact.Error()
				loadErr = errors.New(store.lastErr)
			}
		}
	}
	if errOpen := store.openAppend(); errOpen != nil {
		store.lastErr = "打开请求日志失败: " + errOpen.Error()
		return store, errors.New(store.lastErr)
	}
	return store, loadErr
}

func (s *Store) openAppend() error {
	if s.path == "" {
		return errors.New("请求日志路径为空")
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o700); errMkdir != nil {
		return errMkdir
	}
	file, errOpen := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return errOpen
	}
	s.file = file
	s.writes = 0
	return nil
}

// Append records one attempt. A write failure is reported through LastError and
// never fails the request that produced it.
func (s *Store) Append(entry Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	entry.At = entry.At.UTC()
	if entry.Role == "" {
		entry.Role = s.role
	}
	entry.Version = Version
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%s-%d", entry.Role, time.Now().UnixNano())
	}
	entry.Error = truncate(entry.Error, 300)

	s.entries = append(s.entries, entry)
	if len(s.entries) > MaxEntries {
		s.entries = s.entries[len(s.entries)-MaxEntries:]
	}
	line, errMarshal := json.Marshal(entry)
	if errMarshal != nil {
		s.lastErr = "编码请求日志失败: " + errMarshal.Error()
		return
	}
	if s.file == nil {
		if errOpen := s.openAppend(); errOpen != nil {
			s.lastErr = "打开请求日志失败: " + errOpen.Error()
			return
		}
	}
	if _, errWrite := s.file.Write(append(line, '\n')); errWrite != nil {
		s.lastErr = "写入请求日志失败: " + errWrite.Error()
		return
	}
	s.writes++
	s.lastErr = ""
	if s.writes > MaxEntries*2 || s.fileSizeLocked() > maxFileBytes {
		if errCompact := s.compactLocked(); errCompact != nil {
			s.lastErr = "压缩请求日志失败: " + errCompact.Error()
		}
	}
}

// fileSizeLocked reports the current file size, or zero when it cannot be read.
func (s *Store) fileSizeLocked() int64 {
	if s.file == nil {
		return 0
	}
	info, errStat := s.file.Stat()
	if errStat != nil {
		return 0
	}
	return info.Size()
}

// compactLocked rewrites the file with the in-memory rows. The caller holds the
// lock.
func (s *Store) compactLocked() error {
	if s.path == "" {
		return nil
	}
	buffer := make([]byte, 0, len(s.entries)*256)
	for _, entry := range s.entries {
		line, errMarshal := json.Marshal(entry)
		if errMarshal != nil {
			continue
		}
		buffer = append(buffer, line...)
		buffer = append(buffer, '\n')
	}
	if errMkdir := os.MkdirAll(filepath.Dir(s.path), 0o700); errMkdir != nil {
		return errMkdir
	}
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, buffer, 0o600); errWrite != nil {
		return errWrite
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		return errRename
	}
	return s.openAppend()
}

// Snapshot returns a copy of this face's rows.
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// LastError reports the most recent read/write problem.
func (s *Store) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Shutdown closes the file. It is safe to call more than once.
func (s *Store) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// ReadFile loads one face's records without starting a writer. Lines that do not
// belong to expectedRole are ignored so a renamed file cannot leak across faces.
func ReadFile(path, expectedRole string) ([]Entry, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, errRead
	}
	entries, _ := parseLines(raw, expectedRole)
	now := time.Now()
	kept := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Expired(now) {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) > MaxEntries {
		kept = kept[len(kept)-MaxEntries:]
	}
	return kept, nil
}

// parseLines decodes JSONL rows, skipping corrupt lines and the incomplete last
// line another face may be in the middle of appending.
func parseLines(raw []byte, role string) ([]Entry, int) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	entries := make([]Entry, 0, 128)
	skipped := 0
	lastHadNewline := len(raw) == 0 || raw[len(raw)-1] == '\n'
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var entry Entry
		if errUnmarshal := json.Unmarshal([]byte(trimmed), &entry); errUnmarshal != nil {
			// A torn trailing line is an in-flight append, not corruption.
			if index == len(lines)-1 && !lastHadNewline {
				continue
			}
			skipped++
			continue
		}
		if role != "" && entry.Role != "" && entry.Role != role {
			continue
		}
		if entry.Role == "" {
			entry.Role = role
		}
		entries = append(entries, entry)
	}
	return entries, skipped
}

// truncate bounds one error string and removes newlines so a row stays one line.
func truncate(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
