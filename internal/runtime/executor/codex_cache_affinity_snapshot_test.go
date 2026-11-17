package executor

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"reflect"
	"testing"
	"time"
)

func TestCacheAffinitySnapshotDoesNotMutate(t *testing.T) {
	e := NewCodexExecutor(&config.Config{})
	now := time.Now()
	s := e.affinity
	s.now = func() time.Time { return now }
	snap := codexAffinitySnapshotOf([]byte(`{"input":[{"role":"user","content":"secret"}]}`))
	s.publish("scope", "identity", "group", snap, now)
	s.bindings["identity"] = codexAffinityBinding{group: "group", touched: now}
	before := s.trajectories
	oldBinding := s.bindings["identity"]
	now = now.Add(codexAffinityTTL + time.Second)
	out := e.SnapshotCacheAffinity("scope", "identity", 64)
	if out.Status != "available" || len(out.Rows) != 2 {
		t.Fatalf("snapshot=%+v", out)
	}
	for _, row := range out.Rows {
		if !row.Expired || row.GroupHash == "group" {
			t.Fatalf("row=%+v", row)
		}
	}
	if !reflect.DeepEqual(before, s.trajectories) || s.bindings["identity"] != oldBinding || len(s.trajectories) != 1 {
		t.Fatal("query maintained the index")
	}
	limited := e.SnapshotCacheAffinity("scope", "identity", 1)
	if !limited.Truncated || len(limited.Rows) != 1 {
		t.Fatal("snapshot limit not reported")
	}
	other := e.SnapshotCacheAffinity("other", "other", 64)
	if len(other.Rows) != 0 {
		t.Fatal("scope leaked")
	}
}
