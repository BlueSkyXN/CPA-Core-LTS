package auth

import (
	"testing"
	"time"
)

func TestRecomputeAvailabilityPreservesActiveCredentialQuota(t *testing.T) {
	now := time.Now()
	for _, modelStates := range []map[string]*ModelState{
		nil,
		{"healthy-model": {Status: StatusActive}},
		{"quota-model": {Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute)}}},
	} {
		auth := &Auth{
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Hour),
			Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour), ObservedAt: now, Signals: map[string]string{"X-Codex-Plan-Type": "test"}},
			ModelStates:    modelStates,
		}
		recomputeAggregatedAvailability(auth, now)
		if !auth.Unavailable || !auth.NextRetryAfter.Equal(now.Add(time.Hour)) || auth.Quota.Reason != "credential_quota" || !auth.Quota.NextRecoverAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("active credential quota was replaced by model aggregation: %+v", auth.Quota)
		}
		if !auth.Quota.ObservedAt.Equal(now) || auth.Quota.Signals["X-Codex-Plan-Type"] != "test" {
			t.Fatal("credential quota observation was lost")
		}
	}
}
