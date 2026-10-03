package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// accessProgramNotEnabledResultError mirrors what resultErrorFromError builds
// from a codex terminal statusErr: the HTTP status from StatusCode() and the
// raw upstream JSON body as the message, with no structured Code. errType
// varies because the upstream may pair the specific code with a generic
// error.type; the classification must follow the code either way.
func accessProgramNotEnabledResultError(errType string) *Error {
	body := fmt.Sprintf(`{"error":{"code":"access_program_not_enabled","message":"The cyber access program is not enabled for this account."}}`)
	if errType != "" {
		body = fmt.Sprintf(`{"error":{"type":%q,"code":"access_program_not_enabled","message":"The requested access program is not enabled."}}`, errType)
	}
	return &Error{HTTPStatus: http.StatusForbidden, Message: body}
}

func TestManager_MarkResult_AccessProgramNotEnabledDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, errType := range []string{"", "permission_error", "invalid_request_error"} {
		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-access-program-" + errType, Provider: "codex"}
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}

		model := "gpt-5.6-sol"
		m.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    model,
			Success:  false,
			Error:    accessProgramNotEnabledResultError(errType),
		})

		assertNoCooldown(t, m, auth.ID, model)
	}
}

func TestManager_MarkResult_AccessProgramNotEnabledAuthLevelDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-access-program-auth-level", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// No model key on the result routes the failure to the auth-level branch.
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Success:  false,
		Error:    accessProgramNotEnabledResultError(""),
	})

	assertNoCooldown(t, m, auth.ID, "")
}

func TestManager_MarkResult_OtherForbiddenStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-other-forbidden", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-5.6-sol"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "organization suspended"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("generic 403 must keep the model cooldown, got %#v", state)
	}
}

func TestShouldSkipCredentialCooldown_AccessProgramNotEnabled(t *testing.T) {
	if !shouldSkipCredentialCooldown(accessProgramNotEnabledResultError("")) {
		t.Fatal("access_program_not_enabled must skip credential cooldown")
	}
	// The classification itself must not fire on request-fault program codes or
	// on non-403 statuses carrying the code.
	if isAccessProgramNotEnabledResultError(&Error{
		HTTPStatus: http.StatusForbidden,
		Message:    `{"error":{"code":"unsupported_access_program","message":"mismatched code"}}`,
	}) {
		t.Fatal("request-fault access program codes must not classify as access_program_not_enabled")
	}
	if isAccessProgramNotEnabledResultError(&Error{
		HTTPStatus: http.StatusUnauthorized,
		Message:    `{"error":{"code":"access_program_not_enabled"}}`,
	}) {
		t.Fatal("non-403 status carrying the code must not classify as access_program_not_enabled")
	}
}

// TestExecuteStream_AccessProgramNotEnabledRotatesWithoutCooling pins the
// Daybreak Blue failure contract: a credential whose account has not enabled
// the requested access program returns 403, the conductor rotates to the next
// credential for the same request, and neither the failing credential nor the
// model state is cooled, so the base model stays usable on that credential.
// The upstream may pair the specific code with a generic error.type; rotation
// must follow the code, not collapse into a request-fault stop.
func TestExecuteStream_AccessProgramNotEnabledRotatesWithoutCooling(t *testing.T) {
	for _, errType := range []string{"", "permission_error", "invalid_request_error"} {
		t.Run("type/"+nonEmpty(errType, "absent"), func(t *testing.T) {
			runAccessProgramRotationCase(t, errType)
		})
	}
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func runAccessProgramRotationCase(t *testing.T, errType string) {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(5, 0, 6)

	model := "gpt-access-program-rotation"
	reg := registry.GetGlobalRegistry()
	auths := make([]*Auth, 0, 2)
	for i, priority := range []int{100, 99} {
		id := fmt.Sprintf("auth-access-program-rotate-%s-%d", nonEmpty(errType, "absent"), i+1)
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		auth := &Auth{
			ID:         id,
			Provider:   "codex",
			Status:     StatusActive,
			Attributes: map[string]string{"priority": fmt.Sprintf("%d", priority)},
		}
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
		auths = append(auths, auth)
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})

	var mu sync.Mutex
	order := make([]string, 0, 4)
	nonEnabled := auths[0].ID
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			mu.Lock()
			order = append(order, auth.ID)
			calls := 0
			for _, picked := range order {
				if picked == auth.ID {
					calls++
				}
			}
			mu.Unlock()
			if auth.ID == nonEnabled && calls == 1 {
				return nil, accessProgramNotEnabledResultError(errType)
			}
			return successStreamResult(), nil
		},
	})

	result, errStream := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errStream != nil {
		t.Fatalf("expected rotation to survive a non-enabled credential: %v", errStream)
	}
	if result == nil {
		t.Fatal("expected a stream result from the second credential")
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	mu.Lock()
	rotated := len(order) == 2 && order[0] == nonEnabled && order[1] == auths[1].ID
	mu.Unlock()
	if !rotated {
		t.Fatalf("attempt order = %v, want first credential then rotation", order)
	}
	assertNoCooldown(t, m, nonEnabled, model)

	// The base model must still schedule on the first credential right after
	// the 403: no model cooldown means the highest-priority auth is picked again.
	second, errSecond := m.ExecuteStream(context.Background(), []string{"codex"},
		cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errSecond != nil {
		t.Fatalf("base model request after 403 failed: %v", errSecond)
	}
	if second == nil {
		t.Fatal("expected a stream result on the follow-up request")
	}
	for chunk := range second.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected follow-up chunk error: %v", chunk.Err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[2] != nonEnabled {
		t.Fatalf("follow-up attempt order = %v, want the first credential still eligible", order)
	}
}
