package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchReadsWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer key-good" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"usage":{
			"rolling":{"status":"ok","percent":2,"resetsAt":"2026-11-07T19:11:22.000Z"},
			"weekly":{"status":"ok","percent":41.5,"resetsAt":"2026-11-09T00:00:00.000Z"},
			"monthly":{"status":"limited","percent":100,"resetsAt":"2026-12-04T16:44:04.000Z"},
			"burst":{"status":"ok","percent":7,"resetsAt":"2026-11-08T00:00:00.000Z"}
		}}`))
	}))
	defer server.Close()

	credentials := []Credential{
		{ID: "a", Masked: "oc_sk_good", Key: "key-good", Egress: "", GroupKeys: []string{"g1"}},
		{ID: "b", Masked: "oc_sk_bad", Key: "key-bad", Egress: "", GroupKeys: []string{"g1"}},
	}
	snapshots := Fetch(context.Background(), credentials, FetchOptions{
		UsageURL: server.URL + "/usage",
		Timeout:  5 * time.Second,
	})
	if len(snapshots) != 2 {
		t.Fatalf("snapshots = %d", len(snapshots))
	}
	good := snapshots[0]
	if good.Error != "" {
		t.Fatalf("good key error = %q", good.Error)
	}
	if len(good.Windows) != 4 {
		t.Fatalf("windows = %d, want 4: %+v", len(good.Windows), good.Windows)
	}
	if good.Windows[0].Key != "rolling" || good.Windows[0].Label != "5 小时" || good.Windows[0].Percent != 2 {
		t.Fatalf("rolling window = %+v", good.Windows[0])
	}
	if good.Windows[0].RemainingPercent != 98 {
		t.Fatalf("remaining = %v", good.Windows[0].RemainingPercent)
	}
	if good.Windows[1].Percent != 41.5 || good.Windows[1].RemainingPercent != 58.5 {
		t.Fatalf("weekly window = %+v", good.Windows[1])
	}
	if good.Windows[3].Key != "burst" {
		t.Fatalf("unknown window not appended: %+v", good.Windows[3])
	}
	if good.Egress != "direct" {
		t.Fatalf("egress display = %q, want direct", good.Egress)
	}
	bad := snapshots[1]
	if bad.Error == "" {
		t.Fatal("bad key should carry an error")
	}
}

func TestFetchReportsProtocolErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer server.Close()

	snapshots := Fetch(context.Background(), []Credential{{ID: "a", Masked: "m", Key: "k"}}, FetchOptions{
		UsageURL: server.URL + "/usage",
		Timeout:  time.Second,
	})
	if len(snapshots) != 1 || snapshots[0].Error == "" {
		t.Fatalf("expected a per-key error, got %+v", snapshots)
	}
}

func TestFetchHonoursContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	snapshots := Fetch(ctx, []Credential{{ID: "a", Masked: "m", Key: "k"}}, FetchOptions{
		UsageURL: server.URL + "/usage",
		Timeout:  5 * time.Second,
	})
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancelled request was not absorbed")
	}
	if len(snapshots) != 1 || snapshots[0].Error == "" {
		t.Fatalf("expected an error, got %+v", snapshots)
	}
}
