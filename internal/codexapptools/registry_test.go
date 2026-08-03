package codexapptools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRegistryDefinitionsAndFingerprint(t *testing.T) {
	definitions := Definitions()
	if len(definitions) != 14 {
		t.Fatalf("definition count = %d, want 14", len(definitions))
	}
	wantNames := []string{
		"automation_update",
		"open_in_codex",
		"fork_thread",
		"handoff_thread",
		"get_handoff_status",
		"list_projects",
		"create_thread",
		"list_threads",
		"read_thread",
		"wait_threads",
		"send_message_to_thread",
		"set_thread_pinned",
		"set_thread_archived",
		"set_thread_title",
	}
	if got := Names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("Names() = %v, want %v", got, wantNames)
	}
	for _, definition := range definitions {
		if definition.Name == "" || definition.Description == "" {
			t.Fatalf("incomplete definition: %+v", definition)
		}
		var parameters map[string]any
		if err := json.Unmarshal(definition.Parameters, &parameters); err != nil {
			t.Fatalf("%s parameters: %v", definition.Name, err)
		}
		if parameters["type"] != "object" {
			t.Fatalf("%s parameters type = %v, want object", definition.Name, parameters["type"])
		}
		if definition.Strict {
			t.Fatalf("%s strict = true, want false", definition.Name)
		}
	}
	canonical, err := json.Marshal(struct {
		Bundle string       `json:"bundle"`
		Tools  []Definition `json:"tools"`
	}{Bundle: BundleVersion, Tools: definitions})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != RegistrySchemaSHA256 {
		t.Fatalf("registry fingerprint = %s, want %s", got, RegistrySchemaSHA256)
	}
}

func TestNormalizeSelection(t *testing.T) {
	got, err := NormalizeSelection([]string{" read_thread ", "list_threads", "read_thread"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"list_threads", "read_thread"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeSelection() = %v, want %v", got, want)
	}
	if _, err = NormalizeSelection([]string{"not_a_tool"}); err == nil {
		t.Fatal("NormalizeSelection() accepted unknown tool")
	}
}
