package usage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func sizedUsageRecord() coreusage.Record {
	return coreusage.Record{
		APIKey:      "size-key",
		Model:       "gpt-5.6",
		TraceID:     "a1b2c3d4",
		RequestedAt: time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC),
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: 503, Body: "upstream connect error or disconnect/reset before headers"},
		Detail:      coreusage.Detail{InputTokens: 4, OutputTokens: 1, TotalTokens: 5},
		Size: coreusage.RequestSize{
			ClientRequestBytes:       1048000,
			UpstreamTransport:        "http",
			UpstreamRequestBytes:     1050000,
			UpstreamRequestWireBytes: 1050000,
			UpstreamResponseBytes:    91,
		},
	}
}

func TestRequestStatisticsRecordMapsRequestSize(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), sizedUsageRecord())

	details := stats.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	got := details[0]
	if got.TraceID != "a1b2c3d4" || got.SizeVersion != coreusage.RequestSizeVersionV1 {
		t.Fatalf("trace/version = %q/%d, want a1b2c3d4/1", got.TraceID, got.SizeVersion)
	}
	if got.ClientRequestBytes != 1048000 || got.UpstreamTransport != "http" || got.UpstreamRequestBytes != 1050000 ||
		got.UpstreamRequestWireBytes != 1050000 || got.UpstreamResponseBytes != 91 {
		t.Fatalf("size fields = %+v", got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, field := range []string{`"size_version":1`, `"upstream_transport":"http"`, `"upstream_request_bytes":1050000`, `"trace_id":"a1b2c3d4"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("detail JSON %s missing %s", raw, field)
		}
	}
	for _, field := range []string{"client_request_encoding", "upstream_request_encoding"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("detail JSON %s should omit unmeasured %s", raw, field)
		}
	}
}

func TestRequestStatisticsUnsizedRecordOmitsSizeFields(t *testing.T) {
	record := sizedUsageRecord()
	record.Size = coreusage.RequestSize{}
	record.TraceID = ""
	stats := NewRequestStatistics()
	stats.Record(context.Background(), record)

	raw, err := json.Marshal(stats.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0])
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, field := range []string{"size_version", "trace_id", "_bytes", "upstream_transport"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("detail JSON %s should omit %s", raw, field)
		}
	}
}

func TestRequestSizeSurvivesExportImport(t *testing.T) {
	source := NewRequestStatistics()
	source.Record(context.Background(), sizedUsageRecord())
	raw, err := json.Marshal(source.Snapshot())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var snapshot StatisticsSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	target := NewRequestStatistics()
	if result := requireMergeSnapshot(t, target, snapshot); result.Added != 1 {
		t.Fatalf("added = %d, want 1", result.Added)
	}
	got := target.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0]
	want := source.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details[0]
	if got.requestSize() != want.requestSize() || got.TraceID != want.TraceID {
		t.Fatalf("imported size = %+v trace %q, want %+v trace %q", got.requestSize(), got.TraceID, want.requestSize(), want.TraceID)
	}
}

func TestRequestSizeDoesNotChangeDedupIdentity(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), sizedUsageRecord())

	snapshot := stats.Snapshot()
	details := snapshot.APIs["size-key"].Models["gpt-5.6"].Details
	details[0].TraceID = "ffffffff"
	details[0].UpstreamTransport = "websocket"
	details[0].UpstreamRequestBytes = 7
	details[0].UpstreamResponseBytes = 0
	result := requireMergeSnapshot(t, stats, snapshot)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("merge result = %+v, want duplicate skipped", result)
	}
}

func TestRequestSizeImportNormalisation(t *testing.T) {
	stats := NewRequestStatistics()
	snapshot := StatisticsSnapshot{APIs: map[string]APISnapshot{
		"size-key": {Models: map[string]ModelSnapshot{"gpt-5.6": {Details: []RequestDetail{
			{
				Timestamp:            time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC),
				Source:               "a",
				TraceID:              "../etc",
				SizeVersion:          2,
				UpstreamTransport:    "http",
				UpstreamRequestBytes: 10,
				Tokens:               TokenStats{TotalTokens: 1},
			},
			{
				Timestamp:               time.Date(2026, 10, 11, 9, 0, 1, 0, time.UTC),
				Source:                  "b",
				SizeVersion:             1,
				UpstreamTransport:       "QUIC",
				UpstreamRequestBytes:    -3,
				UpstreamResponseBytes:   12,
				UpstreamRequestEncoding: "zstd",
				Tokens:                  TokenStats{TotalTokens: 1},
			},
			{
				Timestamp:          time.Date(2026, 10, 11, 9, 0, 2, 0, time.UTC),
				Source:             "c",
				ClientRequestBytes: 99,
				Tokens:             TokenStats{TotalTokens: 1},
			},
		}}}},
	}}
	if result := requireMergeSnapshot(t, stats, snapshot); result.Added != 3 {
		t.Fatalf("added = %d, want 3", result.Added)
	}
	bySource := map[string]RequestDetail{}
	for _, detail := range stats.Snapshot().APIs["size-key"].Models["gpt-5.6"].Details {
		bySource[detail.Source] = detail
	}
	if got := bySource["a"]; got.TraceID != "" || got.requestSize() != (coreusage.RequestSize{}) {
		t.Fatalf("unknown size version kept trace %q size %+v, want cleared", got.TraceID, got.requestSize())
	}
	wantB := coreusage.RequestSize{Version: coreusage.RequestSizeVersionV1, UpstreamResponseBytes: 12}
	if got := bySource["b"].requestSize(); got != wantB {
		t.Fatalf("normalised size = %+v, want %+v", got, wantB)
	}
	if got := bySource["c"].requestSize(); got != (coreusage.RequestSize{}) {
		t.Fatalf("unversioned size = %+v, want cleared", got)
	}
}

func TestUsageAnalyticsRequestSizeDistribution(t *testing.T) {
	stats := NewRequestStatistics()
	base := time.UnixMilli(1800000000000)
	add := func(i int, transport string, payload, wire int64, failed bool) {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "size-analytics",
			Model:       "gpt-5.6",
			RequestedAt: base.Add(time.Duration(i) * time.Millisecond),
			Latency:     time.Second,
			Failed:      failed,
			Detail:      coreusage.Detail{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			Size: coreusage.RequestSize{
				UpstreamTransport:        transport,
				UpstreamRequestBytes:     payload,
				UpstreamRequestWireBytes: wire,
			},
		})
	}
	add(0, "http", 1000, 1000, false)
	add(1, "http", 1100000, 1100000, true)
	add(2, "websocket", 1100000, 1100000, false)
	add(3, "http", 0, 300000, false) // encoded body falls back to wire size
	add(4, "", 0, 0, false)          // unmeasured

	cap := stats.QueryCapabilities()
	if cap.RequestSizeVersion != coreusage.RequestSizeVersionV1 {
		t.Fatalf("request_size_version = %d, want 1", cap.RequestSizeVersion)
	}
	got := analyticsRead(t, stats, QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS}).Data.RequestSize
	if got == nil {
		t.Fatal("requestSize missing with measured rows")
	}
	if got.SampleCount != 4 || got.Unmeasured != 1 {
		t.Fatalf("samples/unmeasured = %d/%d, want 4/1", got.SampleCount, got.Unmeasured)
	}
	want := map[string][]int64{
		"requests":          {1, 0, 1, 0, 0, 2, 0},
		"failures":          {0, 0, 0, 0, 0, 1, 0},
		"websocketRequests": {0, 0, 0, 0, 0, 1, 0},
		"websocketFailures": {0, 0, 0, 0, 0, 0, 0},
	}
	for name, series := range map[string][]int64{
		"requests":          got.Requests,
		"failures":          got.Failures,
		"websocketRequests": got.WebsocketRequests,
		"websocketFailures": got.WebsocketFailures,
	} {
		if len(series) != len(got.Edges) {
			t.Fatalf("%s len = %d, want %d", name, len(series), len(got.Edges))
		}
		for i := range series {
			if series[i] != want[name][i] {
				t.Fatalf("%s = %v, want %v", name, series, want[name])
			}
		}
	}
}

func TestUsageAnalyticsOmitsRequestSizeWithoutMeasurements(t *testing.T) {
	stats := queryFixture(3)
	cap := stats.QueryCapabilities()
	raw, err := stats.QueryAnalytics(context.Background(), QueryRequest{Bound: cap.Bound, NowMS: cap.NowMS})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "requestSize") {
		t.Fatalf("analytics without size rows should omit requestSize: %s", raw)
	}
}
