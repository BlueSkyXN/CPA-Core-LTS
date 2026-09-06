package auth

import (
	"context"
	"testing"
	"time"

	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

type flowRebasePreparingExecutor struct {
	*flowFixtureExecutor
	entered chan struct{}
	resume  chan struct{}
}

func (*flowRebasePreparingExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }
func (e *flowRebasePreparingExecutor) PrepareRequestAuth(ctx context.Context, a *Auth) (*Auth, error) {
	close(e.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.resume:
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	a.Metadata["provider_note"] = "prepared"
	return a, nil
}

// Covers the C04 async three-way merge through the real Manager entrypoint,
// with Flow enabled. The fixture never contacts an external provider.
func TestFlowRebasePreparationRetainsConcurrentOperatorEdit(t *testing.T) {
	m, fixture, a := flowFixture(t, flowcontrol.Config{Version: 3, Enabled: true, Rules: []flowcontrol.Rule{
		{ID: "caller", Stage: "request", Scope: "key", MaxConcurrent: 1},
		{ID: "account", Stage: "attempt", Scope: "account", MaxConcurrent: 1},
	}})
	preparing := &flowRebasePreparingExecutor{flowFixtureExecutor: fixture, entered: make(chan struct{}), resume: make(chan struct{})}
	m.RegisterExecutor(preparing)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	close(fixture.finish)
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(ctx, []string{preparing.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{})
		done <- err
	}()
	select {
	case <-preparing.entered:
	case <-ctx.Done():
		t.Fatal("preparation was not reached")
	}
	current, ok := m.GetByID(a.ID)
	if !ok {
		t.Fatal("registered auth missing")
	}
	current.Label = "operator-edit"
	if _, err := m.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	close(preparing.resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Manager did not complete")
	}
	current, _ = m.GetByID(a.ID)
	if current.Label != "operator-edit" || current.Metadata["provider_note"] != "prepared" {
		t.Fatal("Flow integration lost the operator edit or provider preparation delta")
	}
	if current.Failed != 0 || fixture.calls.Load() != 1 {
		t.Fatal("unexpected execution accounting")
	}
	snapshot := m.FlowControlSnapshot()
	if snapshot.Requests != 0 || snapshot.Attempts != 0 || snapshot.Waiting != 0 {
		t.Fatal("completed call retained a Flow permit")
	}
}
