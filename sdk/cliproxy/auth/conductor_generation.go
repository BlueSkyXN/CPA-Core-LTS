package auth

import (
	"context"
	"strings"
)

type authGenerationContextKey struct{}

type authGenerationContextValue struct {
	authID     string
	generation uint64
}

func (m *Manager) nextAuthGenerationLocked() uint64 {
	m.authGeneration++
	if m.authGeneration == 0 {
		m.authGeneration++
	}
	return m.authGeneration
}

func contextWithAuthGeneration(ctx context.Context, auth *Auth) context.Context {
	if auth == nil || auth.ID == "" || auth.generation == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authGenerationContextKey{}, authGenerationContextValue{
		authID:     auth.ID,
		generation: auth.generation,
	})
}

func authGenerationFromContext(ctx context.Context, authID string) uint64 {
	if ctx == nil || authID == "" {
		return 0
	}
	value, ok := ctx.Value(authGenerationContextKey{}).(authGenerationContextValue)
	if !ok || value.authID != authID {
		return 0
	}
	return value.generation
}

func resultForAuth(auth *Auth, provider, model string, success bool, resultErr *Error) Result {
	result := Result{
		Provider: provider,
		Model:    model,
		Success:  success,
		Error:    resultErr,
	}
	if auth != nil {
		result.AuthID = auth.ID
	}
	return result
}

func resultMatchesAuthGeneration(ctx context.Context, result Result, auth *Auth) bool {
	if auth != nil {
		resultProvider := strings.TrimSpace(result.Provider)
		authProvider := strings.TrimSpace(auth.Provider)
		if resultProvider != "" && authProvider != "" && !strings.EqualFold(resultProvider, authProvider) {
			return false
		}
	}
	expectedGeneration := authGenerationFromContext(ctx, result.AuthID)
	if expectedGeneration == 0 {
		return true
	}
	return auth != nil && auth.generation == expectedGeneration
}

func authLifecycleChangedError() *Error {
	return &Error{Code: "auth_lifecycle_changed", Message: "auth changed while preparing request", Retryable: true}
}
