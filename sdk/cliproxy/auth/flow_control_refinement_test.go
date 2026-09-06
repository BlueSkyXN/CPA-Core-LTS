package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

// Integration tests use the real Manager and routing fixtures. They require the
// repository toolchain/dependencies, unlike the standalone Engine test harness.
func TestFlowRejectedConfigKeepsLastGoodManagerPolicyAndRequests(t *testing.T) {
	policy := flowcontrol.Config{Version: 3, Enabled: true, Rules: []flowcontrol.Rule{{
		ID: "limit", Stage: flowcontrol.Attempt, Scope: "global", Model: "flow-model", MaxConcurrent: 3,
		Windows: []flowcontrol.Window{{Requests: 20, PeriodMS: 60000}},
	}}}
	m, e, _ := flowFixture(t, policy)
	close(e.finish)
	run := func() {
		t.Helper()
		if _, err := m.Execute(context.Background(), []string{e.Identifier()}, executor.Request{Model: "flow-model"}, executor.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	before := m.FlowControlPolicy()
	desired := before
	desired.Rules = append([]flowcontrol.Rule(nil), before.Rules...)
	desired.Rules[0].Model = "other-model"
	if err := m.CheckFlowControlConfig(&internalconfig.Config{FlowControl: desired}); err == nil {
		t.Fatal("expected rejection with retained frequency history")
	}
	m.SetConfig(&internalconfig.Config{Debug: true, FlowControl: desired})
	if !m.FlowControlConfigurationError() || m.FlowControlLastUpdateFailure() == nil {
		t.Fatal("missing rejected-update explanation")
	}
	if !reflect.DeepEqual(before, m.FlowControlPolicy()) {
		t.Fatal("engine discarded last-good policy")
	}
	if !reflect.DeepEqual(before, m.runtimeConfigSnapshot().FlowControl) || !m.runtimeConfigSnapshot().Debug {
		t.Fatal("runtime Flow snapshot is not effective or unrelated config did not update")
	}
	run() // An update error is not a global request rejection latch.
	m.SetConfig(&internalconfig.Config{FlowControl: before})
	if m.FlowControlConfigurationError() {
		t.Fatal("successful update did not clear the failure status")
	}
}

func TestFlowResolvedModelDirectoryDoesNotExposeAliasesAsTargetsOrRotatePool(t *testing.T) {
	m := newOpenAICompatPoolTestManager(t, "public-alias", []internalconfig.OpenAICompatibilityModel{
		{Name: "actual-a", Alias: "public-alias"}, {Name: "actual-b", Alias: "public-alias"},
	}, nil)
	defer m.CloseFlowControl()
	before := len(m.modelPoolOffsets)
	first, truncated := m.FlowControlModelOptions()
	second, _ := m.FlowControlModelOptions()
	if truncated || len(first) != 2 || !reflect.DeepEqual(first, second) {
		t.Fatalf("unexpected directory: %+v", first)
	}
	for i, target := range []string{"actual-a", "actual-b"} {
		if first[i].Ref != openAICompatPoolProviderKey+"::"+target || first[i].Model != target {
			t.Fatalf("alias exposed as target: %+v", first[i])
		}
		if !reflect.DeepEqual(first[i].Aliases, []string{"public-alias"}) || len(first[i].Accounts) != 1 {
			t.Fatalf("missing route context: %+v", first[i])
		}
	}
	if len(m.modelPoolOffsets) != before {
		t.Fatal("opening the directory advanced model-pool state")
	}
	cfg := m.runtimeConfigSnapshot().CloneForRuntime()
	cfg.FlowControl = flowcontrol.Config{Version: 3, Enabled: true, Rules: []flowcontrol.Rule{{
		ID: "shared", Stage: flowcontrol.Attempt, Scope: "global", MaxConcurrent: 1,
		Models: []string{first[0].Ref, first[1].Ref},
	}}}
	m.SetConfig(cfg)
	for _, option := range first {
		explanation := m.ExplainFlowControl(flowcontrol.Identity{Stage: flowcontrol.Attempt, Key: "k", Provider: option.Provider, Model: option.Model})
		if len(explanation.Matches) != 1 {
			t.Fatalf("selected execution target did not match rule: %+v", explanation)
		}
	}
}

func TestFlowSequentialStreamCleanupUsesActualProducerCompletion(t *testing.T) {
	m := NewManager(nil, nil, nil)
	defer m.CloseFlowControl()
	m.SetConfig(&internalconfig.Config{FlowControl: flowcontrol.Config{
		Version: 3, Enabled: true, Queue: flowcontrol.QueueConfig{MaxWaiting: 4, MaxWaitMS: 1000},
		Rules: []flowcontrol.Rule{{ID: "a", Stage: flowcontrol.Attempt, Scope: "account", MaxConcurrent: 1}},
	}})
	op := m.flowControl.BeginRequest(flowcontrol.Identity{Stage: flowcontrol.Request, Key: "k"})
	defer op.Release()
	ctx := context.WithValue(context.Background(), flowRequestContextKey{}, op)
	d := flowcontrol.Identity{Stage: flowcontrol.Attempt, Key: "k", Account: "a"}
	permit, err := m.flowControl.AcquireForRequest(ctx, op, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	source := make(chan executor.StreamChunk)
	wrapped := flowcontrol.HoldChannel(ctx, source, permit.Release, nil)
	done := make(chan error, 1)
	go func() { done <- m.discardFlowStreamBeforeRetry(ctx, wrapped) }()
	select {
	case err := <-done:
		t.Fatalf("cleanup completed before producer: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if m.FlowControlSummary().Attempts != 1 {
		t.Fatal("cleanup prematurely released an attempt")
	}
	close(source)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next, err := m.flowControl.AcquireForRequest(ctx, op, d, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}
