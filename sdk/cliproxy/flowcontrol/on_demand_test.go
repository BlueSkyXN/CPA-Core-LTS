package flowcontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func summaryBuildCounts(e *Engine) (encoded, grouped uint64) {
	e.summaryMu.Lock()
	encoded = e.summaryBuilds
	e.summaryMu.Unlock()
	e.mu.Lock()
	grouped = e.summaryGroupBuilds
	e.mu.Unlock()
	return
}

func TestOnDemandSummaryDoesNotGroupOrSampleWithoutSubscribers(t *testing.T) {
	cfg := v3Policy()
	cfg.Observation = ObservationConfig{Realtime: true, IntervalMS: 500}
	e, _ := New(cfg)
	defer e.Close()
	e.active[1] = &active{identity: Identity{Stage: Attempt, Model: "actual", Account: "a"}}
	for n := 0; n < 10; n++ {
		s := e.Summary()
		if s.Attempts != 1 || s.Breakdown != nil {
			t.Fatal("ordinary summary must contain totals only", s)
		}
	}
	// A permitted observer is not a running sampler, even after its interval.
	time.Sleep(550 * time.Millisecond)
	if encoded, grouped := summaryBuildCounts(e); encoded != 0 || grouped != 0 {
		t.Fatal("observation permission or summary triggered sampling", encoded, grouped)
	}
	for _, view := range []summaryView{viewTotal, viewTotal} {
		raw, err := e.cachedSummaryJSON(view)
		if err != nil || bytes.Contains(raw, []byte(`"breakdown"`)) {
			t.Fatal("total view unexpectedly grouped", string(raw), err)
		}
	}
	if encoded, grouped := summaryBuildCounts(e); encoded != 1 || grouped != 0 {
		t.Fatal("total observation built groups or missed cache", encoded, grouped)
	}
}

func TestOnDemandCacheSharesOnlyTheSameViewAndInvalidatesEveryView(t *testing.T) {
	e, _ := New(v3Policy())
	defer e.Close()
	e.active[1] = &active{identity: Identity{Stage: Attempt, Model: "actual", Provider: "p", Account: "a", Key: "k"}}
	views := []summaryView{viewTotal, viewModel, viewAccount, viewKey, viewProvider}
	for _, view := range views {
		first, err := e.cachedSummaryJSON(view)
		if err != nil {
			t.Fatal(err)
		}
		second, _ := e.cachedSummaryJSON(view)
		if !bytes.Equal(first, second) {
			t.Fatal("same view did not share payload", view)
		}
		var frame struct{ State Summary }
		if err := json.Unmarshal(first, &frame); err != nil {
			t.Fatal(err)
		}
		if view == viewTotal {
			if frame.State.Breakdown != nil {
				t.Fatal("total frame contains groups")
			}
		} else if frame.State.Breakdown == nil || frame.State.Breakdown.View != string(view) {
			t.Fatal("different view reused another view's payload", view)
		}
	}
	if encoded, grouped := summaryBuildCounts(e); encoded != 5 || grouped != 4 {
		t.Fatal("cache must be bounded to the five requested views", encoded, grouped)
	}
	policy := e.Policy()
	policy.Enabled = false
	if err := e.Update(policy); err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		raw, _ := e.cachedSummaryJSON(view)
		if !bytes.Contains(raw, []byte(`"enabled":false`)) || !bytes.Contains(raw, []byte(`"policy-revision":2`)) {
			t.Fatal("policy update retained old cached view", view, string(raw))
		}
	}
	if encoded, grouped := summaryBuildCounts(e); encoded != 10 || grouped != 8 {
		t.Fatal("policy update did not invalidate all view caches", encoded, grouped)
	}
}

func TestOnDemandEventsRejectInvalidViewsBeforeSubscribing(t *testing.T) {
	e, _ := New(v3Policy())
	defer e.Close()
	for _, view := range []string{"MODEL", "joint", "model,key", "unknown"} {
		w := httptest.NewRecorder()
		e.ServeEvents(w, httptest.NewRequest("GET", "/events?view="+view, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatal("invalid view was not rejected", view, w.Code)
		}
	}
	if encoded, grouped := summaryBuildCounts(e); encoded != 0 || grouped != 0 {
		t.Fatal("invalid view built a snapshot", encoded, grouped)
	}
	if e.observers != 0 {
		t.Fatal("invalid view consumed observer capacity")
	}
}

func TestOnDemandEventsObserverExitAndViewSwitch(t *testing.T) {
	cfg := v3Policy()
	cfg.Observation = ObservationConfig{Realtime: true, IntervalMS: 500}
	e, _ := New(cfg)
	defer e.Close()
	server := httptest.NewServer(http.HandlerFunc(e.ServeEvents))
	defer server.Close()
	open := func(view string) (*bufio.Scanner, func()) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+view, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			cancel()
			res.Body.Close()
			t.Fatal("events failed", res.StatusCode)
		}
		return bufio.NewScanner(res.Body), func() { cancel(); res.Body.Close() }
	}
	next := func(scan *bufio.Scanner) Summary {
		t.Helper()
		for scan.Scan() {
			if !strings.HasPrefix(scan.Text(), "data: ") {
				continue
			}
			var frame struct{ State Summary }
			if err := json.Unmarshal([]byte(strings.TrimPrefix(scan.Text(), "data: ")), &frame); err != nil {
				t.Fatal(err)
			}
			return frame.State
		}
		t.Fatal("snapshot missing", scan.Err())
		return Summary{}
	}
	countObservers := func(want int) {
		t.Helper()
		waitFor(t, func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.observers == want })
	}
	// Omitted view defaults to total without constructing groups.
	total, stopTotal := open("")
	defer stopTotal()
	if next(total).Breakdown != nil {
		t.Fatal("default SSE view is not total")
	}
	stopTotal()
	countObservers(0)
	a, stopA := open("?view=model")
	defer stopA()
	b, stopB := open("?view=model")
	defer stopB()
	if next(a).Breakdown.View != string(viewModel) || next(b).Breakdown.View != string(viewModel) {
		t.Fatal("model subscription received wrong view")
	}
	before, grouped := summaryBuildCounts(e)
	if before != 2 || grouped != 1 {
		t.Fatal("same-view subscribers must share the initial frame", before, grouped)
	}
	stopA()
	countObservers(1)
	next(b)
	if after, _ := summaryBuildCounts(e); after <= before {
		t.Fatal("remaining observer stopped sampling")
	}
	stopB()
	countObservers(0)
	before, grouped = summaryBuildCounts(e)
	time.Sleep(550 * time.Millisecond)
	if after, groupAfter := summaryBuildCounts(e); after != before || groupAfter != grouped {
		t.Fatal("sampling continued after the final observer exited", before, after)
	}
	account, stopAccount := open("?view=account")
	defer stopAccount()
	if next(account).Breakdown.View != string(viewAccount) {
		t.Fatal("view switch retained model frame")
	}
	stopAccount()
	countObservers(0)
}
