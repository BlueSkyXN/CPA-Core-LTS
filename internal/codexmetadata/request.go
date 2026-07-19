package codexmetadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

const (
	ModeOff    = "off"
	ModeRepair = "repair"
	ModeStrict = "strict"

	WorkspacePolicyPassthrough = "passthrough"
	WorkspacePolicyRedact      = "redact"
	WorkspacePolicyDrop        = "drop"

	maxTurnMetadataBytes          = 128 << 10
	maxTurnMetadataDepth          = 32
	maxWorkspaceCount             = 32
	maxWorkspaceRemoteCount       = 32
	redactedWorkspaceHashSize     = 16
	maxProjectionHeaderValueBytes = 8 << 10
)

var projectionFields = []struct {
	canonical        string
	flat             string
	deleteWhenAbsent bool
}{
	{canonical: "installation_id", flat: "x-codex-installation-id"},
	{canonical: "session_id", flat: "session_id"},
	{canonical: "thread_id", flat: "thread_id"},
	{canonical: "turn_id", flat: "turn_id"},
	{canonical: "window_id", flat: "x-codex-window-id"},
	{canonical: "parent_thread_id", flat: "x-codex-parent-thread-id", deleteWhenAbsent: true},
}

// Policy controls canonical Codex turn metadata normalization.
type Policy struct {
	Mode            string
	WorkspacePolicy string
	Scope           string
}

// State captures the canonical compatibility projections rendered for one request.
type State struct {
	CanonicalPresent bool
	Normalized       bool
	TurnMetadata     string
	SessionID        string
	HasSessionID     bool
	WindowID         string
	HasWindowID      bool
	ParentThreadID   string
	HasParentThread  bool
	Subagent         string
	HasSubagent      bool

	suppressDirectTurnMetadata bool
}

type jsonObjectMember struct {
	key   string
	value json.RawMessage
}

// ValidationError is returned for malformed or conflicting canonical metadata.
// It intentionally carries no untrusted metadata value.
type ValidationError struct {
	Code string
}

func (e *ValidationError) Error() string {
	if e == nil || strings.TrimSpace(e.Code) == "" {
		return "invalid Codex client metadata"
	}
	return "invalid Codex client metadata: " + e.Code
}

// NormalizeRequest treats body client_metadata.x-codex-turn-metadata as the
// canonical source, applies workspace privacy policy, and regenerates flat
// compatibility projections. Header projections are applied separately through
// State.ApplyHeaders after all provider/model header overrides have run.
func NormalizeRequest(body []byte, directTurnMetadata string, policy Policy) ([]byte, State, error) {
	mode := normalizeMode(policy.Mode)
	if mode == ModeOff {
		return body, detectCanonicalState(body, directTurnMetadata), nil
	}
	directTurnMetadata = strings.TrimSpace(directTurnMetadata)

	clientMetadata, clientMetadataExists, err := clientMetadataObject(body)
	if err != nil {
		if directTurnMetadata == "" {
			if validationErr, ok := err.(*ValidationError); ok && validationErr.Code == "client metadata must be an object" {
				return body, State{}, nil
			}
		}
		return nil, State{}, err
	}
	rawCanonical, bodyCanonicalExists := clientMetadata["x-codex-turn-metadata"]
	if !bodyCanonicalExists && directTurnMetadata == "" {
		return body, State{}, nil
	}

	var canonicalText string
	if bodyCanonicalExists {
		if err = json.Unmarshal(rawCanonical, &canonicalText); err != nil {
			return nil, State{}, &ValidationError{Code: "canonical metadata must be a JSON string"}
		}
	} else {
		canonicalText = directTurnMetadata
		if !clientMetadataExists {
			clientMetadata = make(map[string]json.RawMessage)
		}
	}
	canonical, canonicalValue, err := decodeCanonicalObject(canonicalText)
	if err != nil {
		return nil, State{}, err
	}
	requestKind, canonicalMarker := canonicalString(canonical, "request_kind")
	if !canonicalMarker || strings.TrimSpace(requestKind) == "" {
		return body, State{}, nil
	}
	state := State{CanonicalPresent: true}
	if err = validateCanonicalProjectionTypes(canonical); err != nil {
		return nil, state, err
	}
	subagent, hasSubagent, err := headerProjectionJSONString(clientMetadata, "x-openai-subagent")
	if err != nil {
		return nil, state, err
	}
	if mode == ModeStrict {
		if projectionConflict(clientMetadata, canonical) {
			return nil, state, &ValidationError{Code: "canonical and flat projections conflict"}
		}
		if bodyCanonicalExists && directTurnMetadata != "" {
			_, directValue, errDirect := decodeCanonicalObject(directTurnMetadata)
			if errDirect != nil {
				return nil, state, &ValidationError{Code: "direct canonical header is malformed"}
			}
			if !reflect.DeepEqual(canonicalValue, directValue) {
				return nil, state, &ValidationError{Code: "body and direct canonical metadata conflict"}
			}
		}
	}

	workspaceScope := strings.TrimSpace(policy.Scope)
	if installationID, ok := canonicalString(canonical, "installation_id"); ok {
		workspaceScope += "\x00installation:" + installationID
	}
	if err = applyWorkspacePolicy(canonical, normalizeWorkspacePolicy(policy.WorkspacePolicy), workspaceScope); err != nil {
		return nil, state, err
	}
	canonicalJSON, err := marshalASCIIJSON(canonical)
	if err != nil {
		return nil, state, &ValidationError{Code: "canonical metadata cannot be rendered"}
	}
	canonicalText = string(canonicalJSON)
	encodedCanonical, err := json.Marshal(canonicalText)
	if err != nil {
		return nil, state, &ValidationError{Code: "canonical metadata cannot be embedded"}
	}
	clientMetadata["x-codex-turn-metadata"] = encodedCanonical
	regenerateFlatProjections(clientMetadata, canonical)

	clientMetadataJSON, err := json.Marshal(clientMetadata)
	if err != nil {
		return nil, state, &ValidationError{Code: "client metadata cannot be rendered"}
	}
	updatedBody, err := replaceClientMetadata(body, clientMetadataJSON)
	if err != nil {
		return nil, state, &ValidationError{Code: "client metadata cannot be applied"}
	}

	state.Normalized = true
	state.TurnMetadata = canonicalText
	state.SessionID, state.HasSessionID = canonicalSessionID(canonical)
	state.WindowID, state.HasWindowID = canonicalString(canonical, "window_id")
	state.ParentThreadID, state.HasParentThread = canonicalString(canonical, "parent_thread_id")
	state.Subagent, state.HasSubagent = subagent, hasSubagent
	return updatedBody, state, nil
}

func detectCanonicalState(body []byte, directTurnMetadata string) State {
	state, bodyCanonical, bodyUnique := detectBodyCanonicalState(body)
	if state.CanonicalPresent {
		if bodyUnique {
			populateOffModeSessionState(&state, bodyCanonical)
		}
		return state
	}

	directCanonical, _, err := decodeCanonicalObject(strings.TrimSpace(directTurnMetadata))
	if err != nil {
		return state
	}
	requestKind, exists := canonicalString(directCanonical, "request_kind")
	if !exists || strings.TrimSpace(requestKind) == "" {
		return state
	}
	state.CanonicalPresent = true
	populateOffModeSessionState(&state, directCanonical)
	return state
}

func detectBodyCanonicalState(body []byte) (State, map[string]json.RawMessage, bool) {
	var state State
	carriers, _ := clientMetadataCarriers(body)
	ambiguous := len(carriers) > 1
	canonicalCount := 0
	var unique map[string]json.RawMessage
	for _, carrier := range carriers {
		members, err := decodeJSONObjectMembers(carrier)
		if err != nil {
			continue
		}
		seen := make(map[string]struct{}, len(members))
		carrierDuplicate := false
		for _, member := range members {
			if _, duplicate := seen[member.key]; duplicate {
				carrierDuplicate = true
			}
			seen[member.key] = struct{}{}
			if member.key != "x-codex-turn-metadata" {
				continue
			}
			var canonicalText string
			if json.Unmarshal(member.value, &canonicalText) != nil {
				continue
			}
			canonical, _, errCanonical := decodeCanonicalObject(strings.TrimSpace(canonicalText))
			if errCanonical != nil {
				continue
			}
			requestKind, exists := canonicalString(canonical, "request_kind")
			if !exists || strings.TrimSpace(requestKind) == "" {
				continue
			}
			state.CanonicalPresent = true
			canonicalCount++
			unique = canonical
		}
		if carrierDuplicate {
			ambiguous = true
		}
	}
	state.suppressDirectTurnMetadata = state.CanonicalPresent
	return state, unique, state.CanonicalPresent && !ambiguous && canonicalCount == 1
}

func populateOffModeSessionState(state *State, canonical map[string]json.RawMessage) {
	if state == nil || canonical == nil {
		return
	}
	if validateCanonicalProjectionTypes(canonical) != nil {
		return
	}
	state.SessionID, state.HasSessionID = canonicalSessionID(canonical)
}

// SuppressDirectTurnMetadata reports whether an off-mode body canonical source
// must take precedence over a separately supplied direct canonical header.
func (s State) SuppressDirectTurnMetadata() bool {
	return s.suppressDirectTurnMetadata
}

// ApplyHeaders regenerates direct compatibility headers from the normalized
// canonical object. It is a no-op for off mode and legacy requests.
func (s State) ApplyHeaders(headers http.Header) {
	if headers == nil || !s.Normalized {
		return
	}
	setHeaderCaseInsensitive(headers, "X-Codex-Turn-Metadata", s.TurnMetadata)
	if s.HasWindowID {
		setHeaderCaseInsensitive(headers, "X-Codex-Window-Id", s.WindowID)
	}
	if s.HasParentThread {
		setHeaderCaseInsensitive(headers, "X-Codex-Parent-Thread-Id", s.ParentThreadID)
	} else {
		deleteHeaderCaseInsensitive(headers, "X-Codex-Parent-Thread-Id")
	}
	if s.HasSubagent {
		setHeaderCaseInsensitive(headers, "X-OpenAI-Subagent", s.Subagent)
	} else {
		deleteHeaderCaseInsensitive(headers, "X-OpenAI-Subagent")
	}
}

func clientMetadataObject(body []byte) (map[string]json.RawMessage, bool, error) {
	carriers, err := clientMetadataCarriers(body)
	if err != nil {
		return nil, false, &ValidationError{Code: "request body is malformed"}
	}
	if len(carriers) == 0 {
		return nil, false, nil
	}
	if len(carriers) != 1 {
		return nil, true, &ValidationError{Code: "duplicate client metadata carriers"}
	}
	raw := carriers[0]
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	members, err := decodeJSONObjectMembers(raw)
	if err != nil {
		return nil, true, &ValidationError{Code: "client metadata must be an object"}
	}
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if _, duplicate := seen[member.key]; duplicate {
			return nil, true, &ValidationError{Code: "client metadata contains duplicate keys"}
		}
		seen[member.key] = struct{}{}
	}
	var clientMetadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &clientMetadata); err != nil || clientMetadata == nil {
		return nil, true, &ValidationError{Code: "client metadata must be an object"}
	}
	return clientMetadata, true, nil
}

func clientMetadataCarriers(body []byte) ([]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	members, err := decodeJSONObjectMembers(body)
	carriers := make([]json.RawMessage, 0, 1)
	for _, member := range members {
		if member.key == "client_metadata" {
			carriers = append(carriers, member.value)
		}
	}
	return carriers, err
}

func requestEnvelope(body []byte) (map[string]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return nil, &ValidationError{Code: "request body is malformed"}
	}
	return envelope, nil
}

func decodeJSONObjectMembers(raw []byte) ([]jsonObjectMember, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if start != json.Delim('{') {
		return nil, fmt.Errorf("JSON value is not an object")
	}
	members := make([]jsonObjectMember, 0, 8)
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		if errKey != nil {
			return members, errKey
		}
		key, ok := keyToken.(string)
		if !ok {
			return members, fmt.Errorf("JSON object key is not a string")
		}
		members = append(members, jsonObjectMember{key: key})
		if err = decoder.Decode(&members[len(members)-1].value); err != nil {
			return members, err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return members, err
	}
	if end != json.Delim('}') {
		return members, fmt.Errorf("JSON object is not terminated")
	}
	if _, err = decoder.Token(); err != io.EOF {
		if err == nil {
			return members, fmt.Errorf("JSON object has trailing data")
		}
		return members, err
	}
	return members, nil
}

func replaceClientMetadata(body, clientMetadataJSON []byte) ([]byte, error) {
	envelope, err := requestEnvelope(body)
	if err != nil {
		return nil, err
	}
	if envelope == nil {
		return nil, &ValidationError{Code: "request body is malformed"}
	}
	envelope["client_metadata"] = bytes.Clone(clientMetadataJSON)
	return json.Marshal(envelope)
}

func decodeCanonicalObject(raw string) (map[string]json.RawMessage, any, error) {
	if len(raw) == 0 || len(raw) > maxTurnMetadataBytes {
		return nil, nil, &ValidationError{Code: "canonical metadata size is invalid"}
	}
	data := []byte(raw)
	if err := validateJSON(data, maxTurnMetadataDepth); err != nil {
		return nil, nil, &ValidationError{Code: "canonical metadata is malformed"}
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, nil, &ValidationError{Code: "canonical metadata must be an object"}
	}
	var semantic any
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&semantic); err != nil {
		return nil, nil, &ValidationError{Code: "canonical metadata is malformed"}
	}
	if _, ok := semantic.(map[string]any); !ok {
		return nil, nil, &ValidationError{Code: "canonical metadata must be an object"}
	}
	return object, semantic, nil
}

func validateJSON(raw []byte, maxDepth int) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0, maxDepth); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("JSON depth exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, errKey := decoder.Token()
			if errKey != nil {
				return errKey
			}
			key, okKey := keyToken.(string)
			if !okKey {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key")
			}
			seen[key] = struct{}{}
			if err = validateJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, errEnd := decoder.Token()
		if errEnd != nil || end != json.Delim('}') {
			return fmt.Errorf("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err = validateJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, errEnd := decoder.Token()
		if errEnd != nil || end != json.Delim(']') {
			return fmt.Errorf("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}

func projectionConflict(clientMetadata, canonical map[string]json.RawMessage) bool {
	for _, field := range projectionFields {
		flatRaw, exists := clientMetadata[field.flat]
		if !exists {
			continue
		}
		canonicalValue, canonicalExists := canonicalString(canonical, field.canonical)
		if !canonicalExists {
			if field.deleteWhenAbsent {
				return true
			}
			continue
		}
		var flatValue string
		if err := json.Unmarshal(flatRaw, &flatValue); err != nil || flatValue != canonicalValue {
			return true
		}
	}
	return false
}

func validateCanonicalProjectionTypes(canonical map[string]json.RawMessage) error {
	for _, field := range projectionFields {
		raw, exists := canonical[field.canonical]
		if !exists {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return &ValidationError{Code: "canonical " + field.canonical + " must be a string"}
		}
		if err := validateHeaderProjectionValue(field.canonical, value); err != nil {
			return err
		}
	}
	return nil
}

func validateHeaderProjectionValue(name, value string) error {
	if len(value) > maxProjectionHeaderValueBytes {
		return &ValidationError{Code: "canonical " + name + " exceeds header projection limit"}
	}
	if strings.TrimSpace(value) != value {
		return &ValidationError{Code: "canonical " + name + " contains surrounding whitespace"}
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] == 0x7f {
			return &ValidationError{Code: "canonical " + name + " contains control characters"}
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return &ValidationError{Code: "canonical " + name + " contains control characters"}
		}
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return &ValidationError{Code: "canonical " + name + " is not a valid header value"}
	}
	return nil
}

func regenerateFlatProjections(clientMetadata, canonical map[string]json.RawMessage) {
	for _, field := range projectionFields {
		value, exists := canonicalString(canonical, field.canonical)
		if !exists {
			if field.deleteWhenAbsent {
				delete(clientMetadata, field.flat)
			}
			continue
		}
		encoded, err := json.Marshal(value)
		if err == nil {
			clientMetadata[field.flat] = encoded
		}
	}
}

func canonicalString(object map[string]json.RawMessage, key string) (string, bool) {
	raw, exists := object[key]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func canonicalSessionID(canonical map[string]json.RawMessage) (string, bool) {
	for _, key := range []string{"session_id", "thread_id"} {
		value, exists := canonicalString(canonical, key)
		if value = strings.TrimSpace(value); exists && value != "" {
			return value, true
		}
	}
	return "", false
}

func headerProjectionJSONString(object map[string]json.RawMessage, key string) (string, bool, error) {
	raw, exists := object[key]
	if !exists {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, &ValidationError{Code: key + " must be a string"}
	}
	if value == "" {
		return "", false, nil
	}
	if err := validateHeaderProjectionValue(key, value); err != nil {
		return "", false, err
	}
	return value, true, nil
}

func applyWorkspacePolicy(canonical map[string]json.RawMessage, policy, scope string) error {
	rawWorkspaces, exists := canonical["workspaces"]
	if !exists || bytes.Equal(bytes.TrimSpace(rawWorkspaces), []byte("null")) {
		return nil
	}
	if policy == WorkspacePolicyDrop {
		delete(canonical, "workspaces")
		return nil
	}

	var workspaces map[string]json.RawMessage
	if err := json.Unmarshal(rawWorkspaces, &workspaces); err != nil || workspaces == nil {
		return &ValidationError{Code: "workspace metadata is malformed"}
	}
	if len(workspaces) > maxWorkspaceCount {
		return &ValidationError{Code: "workspace count exceeds limit"}
	}

	normalized := make(map[string]json.RawMessage, len(workspaces))
	for workspacePath, rawWorkspace := range workspaces {
		var workspace map[string]json.RawMessage
		if err := json.Unmarshal(rawWorkspace, &workspace); err != nil || workspace == nil {
			return &ValidationError{Code: "workspace entry is malformed"}
		}
		if policy == WorkspacePolicyRedact {
			redacted := make(map[string]json.RawMessage, 1)
			if rawHasChanges, ok := workspace["has_changes"]; ok {
				var hasChanges bool
				if json.Unmarshal(rawHasChanges, &hasChanges) == nil {
					redacted["has_changes"] = rawHasChanges
				}
			}
			workspace = redacted
			workspacePath = redactedWorkspaceKey(scope, workspacePath)
		} else if err := sanitizeWorkspaceRemotes(workspace); err != nil {
			return err
		}
		renderedWorkspace, err := json.Marshal(workspace)
		if err != nil {
			return &ValidationError{Code: "workspace entry cannot be rendered"}
		}
		normalized[workspacePath] = renderedWorkspace
	}
	renderedWorkspaces, err := json.Marshal(normalized)
	if err != nil {
		return &ValidationError{Code: "workspace metadata cannot be rendered"}
	}
	canonical["workspaces"] = renderedWorkspaces
	return nil
}

func sanitizeWorkspaceRemotes(workspace map[string]json.RawMessage) error {
	rawRemotes, exists := workspace["associated_remote_urls"]
	if !exists || bytes.Equal(bytes.TrimSpace(rawRemotes), []byte("null")) {
		return nil
	}
	var remotes map[string]json.RawMessage
	if err := json.Unmarshal(rawRemotes, &remotes); err != nil || remotes == nil {
		return &ValidationError{Code: "workspace remotes are malformed"}
	}
	if len(remotes) > maxWorkspaceRemoteCount {
		return &ValidationError{Code: "workspace remote count exceeds limit"}
	}
	for name, rawRemote := range remotes {
		var remote string
		if err := json.Unmarshal(rawRemote, &remote); err != nil {
			delete(remotes, name)
			continue
		}
		encoded, err := json.Marshal(sanitizeGitRemote(remote))
		if err != nil {
			delete(remotes, name)
			continue
		}
		remotes[name] = encoded
	}
	rendered, err := json.Marshal(remotes)
	if err != nil {
		return &ValidationError{Code: "workspace remotes cannot be rendered"}
	}
	workspace["associated_remote_urls"] = rendered
	return nil
}

func sanitizeGitRemote(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		if colon := strings.Index(trimmed, ":"); colon > 0 {
			prefix := trimmed[:colon]
			if at := strings.LastIndex(prefix, "@"); at >= 0 && at+1 < len(prefix) {
				host := strings.TrimSpace(prefix[at+1:])
				path := stripQueryAndFragment(strings.TrimLeft(trimmed[colon+1:], "/"))
				if host != "" && path != "" {
					return "ssh://" + host + "/" + path
				}
			}
		}
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Scheme != "" {
		if parsed.Host == "" && parsed.Opaque != "" {
			return "redacted:remote"
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		sanitized := parsed.String()
		if strings.Contains(sanitized, "@") {
			return "redacted:remote"
		}
		return sanitized
	}
	withoutQuery := stripQueryAndFragment(trimmed)
	if strings.Contains(withoutQuery, "@") {
		return "redacted:remote"
	}
	return withoutQuery
}

func stripQueryAndFragment(value string) string {
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		return value[:index]
	}
	return value
}

func redactedWorkspaceKey(scope, workspacePath string) string {
	sum := sha256.Sum256([]byte("cpa:codex:workspace:v1\x00" + strings.TrimSpace(scope) + "\x00" + workspacePath))
	return "workspace:" + hex.EncodeToString(sum[:redactedWorkspaceHashSize])
}

func marshalASCIIJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if utf8.Valid(raw) && isASCII(raw) {
		return raw, nil
	}
	out := make([]byte, 0, len(raw)+16)
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8")
		}
		if r <= 0x7f {
			out = append(out, byte(r))
		} else if r <= 0xffff {
			out = fmt.Appendf(out, "\\u%04x", r)
		} else {
			high, low := utf16.EncodeRune(r)
			out = fmt.Appendf(out, "\\u%04x\\u%04x", high, low)
		}
		raw = raw[size:]
	}
	return out, nil
}

func isASCII(raw []byte) bool {
	for _, value := range raw {
		if value > 0x7f {
			return false
		}
	}
	return true
}

func normalizeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ModeOff:
		return ModeOff
	case ModeStrict:
		return ModeStrict
	default:
		return ModeRepair
	}
}

func normalizeWorkspacePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case WorkspacePolicyRedact:
		return WorkspacePolicyRedact
	case WorkspacePolicyDrop:
		return WorkspacePolicyDrop
	default:
		return WorkspacePolicyPassthrough
	}
}

func setHeaderCaseInsensitive(headers http.Header, key, value string) {
	deleteHeaderCaseInsensitive(headers, key)
	headers.Set(key, value)
}

func deleteHeaderCaseInsensitive(headers http.Header, key string) {
	for existing := range headers {
		if strings.EqualFold(existing, key) {
			delete(headers, existing)
		}
	}
}
