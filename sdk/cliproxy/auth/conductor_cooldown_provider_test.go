package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestManager_RestoreCooldownRecordProviderCompatibility(t *testing.T) {
	now := time.Now().UTC()
	nextRetry := now.Add(time.Hour)

	tests := []struct {
		name           string
		recordProvider string
		model          string
		wantRestored   bool
	}{
		{name: "auth level provider mismatch", recordProvider: "claude", wantRestored: false},
		{name: "model level provider mismatch", recordProvider: "claude", model: "grok-4", wantRestored: false},
		{name: "auth level legacy empty provider", recordProvider: "", wantRestored: true},
		{name: "model level legacy empty provider", recordProvider: "", model: "grok-4", wantRestored: true},
		{name: "auth level matching provider is case insensitive", recordProvider: " XAI ", wantRestored: true},
		{name: "model level matching provider is case insensitive", recordProvider: "Xai", model: "grok-4", wantRestored: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			const authID = "restore-provider-compatibility"
			if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
				ID:       authID,
				Provider: "xai",
				Status:   StatusActive,
			}); err != nil {
				t.Fatalf("register auth: %v", err)
			}

			record := CooldownStateRecord{
				Provider:       test.recordProvider,
				AuthID:         authID,
				Model:          test.model,
				NextRetryAfter: nextRetry,
				Reason:         "quota",
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
				UpdatedAt: now,
			}

			manager.mu.Lock()
			restored := manager.restoreCooldownRecordLocked(record, now)
			manager.mu.Unlock()
			if restored != test.wantRestored {
				t.Fatalf("restoreCooldownRecordLocked() = %v, want %v", restored, test.wantRestored)
			}

			auth, ok := manager.GetByID(authID)
			if !ok || auth == nil {
				t.Fatal("auth missing after cooldown restore")
			}
			if !test.wantRestored {
				if auth.Status != StatusActive || auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.LastError != nil || len(auth.ModelStates) != 0 {
					t.Fatalf("provider mismatch mutated auth: %#v", auth)
				}
				return
			}
			if test.model == "" {
				if auth.Status != StatusError || !auth.Unavailable || !auth.NextRetryAfter.Equal(nextRetry) || auth.LastError == nil {
					t.Fatalf("legacy auth-level cooldown was not restored: %#v", auth)
				}
				return
			}
			state := auth.ModelStates[test.model]
			if state == nil || state.Status != StatusError || !state.Unavailable || !state.NextRetryAfter.Equal(nextRetry) || state.LastError == nil {
				t.Fatalf("legacy model-level cooldown was not restored: %#v", state)
			}
		})
	}
}
