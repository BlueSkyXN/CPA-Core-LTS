package auth

import (
	"reflect"
	"testing"
	"time"
)

func TestAsyncMergePreservesOperatorEditsAndRotatesTokens(t *testing.T) {
	base := &Auth{ID: "a", Provider: "codex", ProxyURL: "old", Label: "old", Metadata: map[string]any{
		"access_token": "old", "refresh_token": "old-r", "nested": map[string]any{"note": "old", "expiry": 1},
	}, Attributes: map[string]string{"weight": "1", "remove": "old"}, Status: StatusActive}
	current, updated := base.Clone(), base.Clone()
	current.ProxyURL = "new-proxy"
	current.Label = "operator"
	current.Attributes["weight"] = "7"
	current.Metadata["nested"].(map[string]any)["note"] = "edited"
	updated.Metadata["access_token"] = "new"
	updated.Metadata["refresh_token"] = "new-r"
	updated.Metadata["nested"].(map[string]any)["expiry"] = 2
	delete(updated.Attributes, "remove")
	got := mergeAsyncAuth(base, current, updated)
	if got.ProxyURL != "new-proxy" || got.Label != "operator" || got.Attributes["weight"] != "7" {
		t.Fatalf("operator edits lost: %+v", got)
	}
	if got.Metadata["access_token"] != "new" || got.Metadata["refresh_token"] != "new-r" {
		t.Fatal("refresh not applied")
	}
	if _, ok := got.Attributes["remove"]; ok {
		t.Fatal("provider deletion lost")
	}
	if !reflect.DeepEqual(got.Metadata["nested"], map[string]any{"note": "edited", "expiry": 2}) {
		t.Fatalf("nested merge: %#v", got.Metadata)
	}
}

func TestAsyncMergeDoesNotMixTokenFamilies(t *testing.T) {
	base := &Auth{Metadata: map[string]any{"access_token": "a", "refresh_token": "r"}}
	current, updated := base.Clone(), base.Clone()
	current.Metadata["access_token"] = "manual-a"
	updated.Metadata["access_token"] = "rotated-a"
	updated.Metadata["refresh_token"] = "rotated-r"
	got := mergeAsyncAuth(base, current, updated)
	if !reflect.DeepEqual(got.Metadata, current.Metadata) {
		t.Fatalf("mixed credential family: %#v", got.Metadata)
	}
}

func TestAsyncMergePreservesNewerAvailability(t *testing.T) {
	base := &Auth{Status: StatusError, StatusMessage: "old-error", LastError: &Error{Code: "old"}, ModelStates: map[string]*ModelState{"m": {Status: StatusActive}}}
	current, updated := base.Clone(), base.Clone()
	current.LastError = &Error{Code: "rate_limit"}
	current.NextRetryAfter = time.Unix(999999, 0)
	current.Unavailable = true
	current.ModelStates["m"].Status = StatusError
	updated.Status = StatusActive
	updated.StatusMessage = ""
	updated.LastError = nil
	updated.ModelStates["m"].Status = StatusActive
	got := mergeAsyncAuth(base, current, updated)
	if !got.Unavailable || got.LastError.Code != "rate_limit" || got.NextRetryAfter != current.NextRetryAfter || got.Status != current.Status {
		t.Fatalf("newer failure erased: %+v", got)
	}
	if got.ModelStates["m"].Status != StatusError {
		t.Fatal("model cooldown overwritten")
	}
}

func TestAsyncMergePreservesDeletionAndAdditionConflicts(t *testing.T) {
	base := map[string]string{"delete": "a", "same": "base"}
	current := map[string]string{"same": "operator", "new": "operator"}
	updated := map[string]string{"delete": "provider", "same": "provider", "new": "provider", "fresh": "added"}
	want := map[string]string{"same": "operator", "new": "operator", "fresh": "added"}
	if got := mergeAuthMap(base, current, updated); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestAuthCloneOwnsNestedAndEmptyCollections(t *testing.T) {
	source := &Auth{Metadata: map[string]any{"nested": map[string]any{"a": []any{map[string]any{"b": "old"}}}}, Attributes: map[string]string{}, ModelStates: map[string]*ModelState{}, LastError: &Error{Message: "old"}}
	clone := source.Clone()
	clone.Metadata["nested"].(map[string]any)["a"].([]any)[0].(map[string]any)["b"] = "new"
	clone.Attributes["new"] = "new"
	clone.ModelStates["new"] = &ModelState{}
	clone.LastError.Message = "new"
	if source.Metadata["nested"].(map[string]any)["a"].([]any)[0].(map[string]any)["b"] != "old" || len(source.Attributes) > 0 || len(source.ModelStates) > 0 || source.LastError.Message != "old" {
		t.Fatal("clone aliases original")
	}
}
