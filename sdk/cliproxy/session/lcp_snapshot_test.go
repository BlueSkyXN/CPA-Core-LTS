package session

import (
	"reflect"
	"testing"
	"time"
)

func TestPrefixSnapshotDoesNotTouchTTLOrLRU(t *testing.T) {
	m := NewMerklePrefixMatcher(time.Hour)
	now := time.Now()
	m.nowFunc = func() time.Time { return now }
	id := m.BindFingerprints("namespace", []string{"one", "two"}, 1, "auth")
	var before lcpGroup
	for _, g := range m.groups["namespace"].groups {
		before = *g
	}
	operations, counter := m.operations, m.accessCounter
	now = now.Add(2 * time.Hour)
	rows, _ := m.SnapshotSession(snapshotHash(id), "namespace", 1)
	if len(rows) != 1 || !rows[0].Expired {
		t.Fatalf("rows=%+v", rows)
	}
	if m.operations != operations || m.accessCounter != counter {
		t.Fatal("query altered access order")
	}
	for _, g := range m.groups["namespace"].groups {
		if !reflect.DeepEqual(before, *g) {
			t.Fatal("query changed trajectory")
		}
	}
	if m.groupCount != 1 {
		t.Fatal("query expired trajectory")
	}
}
