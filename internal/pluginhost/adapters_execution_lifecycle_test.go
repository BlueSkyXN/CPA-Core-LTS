package pluginhost

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type executionResultHook struct {
	results []coreauth.Result
}

func (*executionResultHook) OnAuthRegistered(context.Context, *coreauth.Auth) {}

func (*executionResultHook) OnAuthUpdated(context.Context, *coreauth.Auth) {}

func (h *executionResultHook) OnResult(_ context.Context, result coreauth.Result) {
	h.results = append(h.results, result)
}

func TestExecutorAdapterMapsLifecycleIdentity(t *testing.T) {
	host := NewForTest(nil)
	var captured pluginapi.ExecutorRequest
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			captured = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"lifecycle-identity",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	req := coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI, Payload: []byte(`{"input":"hello"}`)}
	opts := coreexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			coreexecutor.RequestIDMetadataKey:        "request-1",
			coreexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}

	if _, errExecute := adapter.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if captured.RequestID != "request-1" || captured.ExecutionSessionID != "session-1" || captured.AuthID != "auth-1" || captured.AuthIndex != "index-1" {
		t.Fatalf("lifecycle identity = request:%q session:%q auth:%q index:%q", captured.RequestID, captured.ExecutionSessionID, captured.AuthID, captured.AuthIndex)
	}

	opts.Metadata = map[string]any{
		coreexecutor.RequestIDMetadataKey:        "request-2",
		coreexecutor.DerivedSessionIDMetadataKey: "derived-session-2",
	}
	if _, errExecute := adapter.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() with derived session error = %v", errExecute)
	}
	if captured.RequestID != "request-2" || captured.ExecutionSessionID != "" {
		t.Fatalf("affinity-only identity = request:%q session:%q, want no typed execution session", captured.RequestID, captured.ExecutionSessionID)
	}
}

func TestExecutorAdapterCancelsStreamOnContextDone(t *testing.T) {
	host := NewForTest(nil)
	pluginChunks := make(chan pluginapi.ExecutorStreamChunk)
	cancelRequests := make(chan pluginapi.CancelExecutionRequest, 1)
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: pluginChunks}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"lifecycle-cancel",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.canceller = executionCancellerFunc(func(_ context.Context, req pluginapi.CancelExecutionRequest) error {
		cancelRequests <- req
		return nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	req := coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.RequestIDMetadataKey:        "request-cancel",
		coreexecutor.ExecutionSessionMetadataKey: "session-cancel",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result, errStream := adapter.ExecuteStream(ctx, auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	cancel()
	select {
	case _, ok := <-result.Chunks:
		if ok {
			t.Fatal("stream emitted a chunk after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close after cancellation")
	}
	select {
	case cancelReq := <-cancelRequests:
		if cancelReq.RequestID != "request-cancel" || cancelReq.ExecutionSessionID != "session-cancel" || cancelReq.AuthID != "auth-1" || cancelReq.AuthIndex != "index-1" || cancelReq.Reason != pluginapi.ExecutionCancelReasonContextCanceled {
			t.Fatalf("cancel request = %#v", cancelReq)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin did not receive explicit cancellation")
	}
}

func TestExecutorAdapterCancelsNonStreamOnContextDone(t *testing.T) {
	host := NewForTest(nil)
	started := make(chan struct{})
	cancelRequests := make(chan pluginapi.CancelExecutionRequest, 1)
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, _ pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			close(started)
			<-ctx.Done()
			return pluginapi.ExecutorResponse{}, ctx.Err()
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"lifecycle-cancel-nonstream",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.canceller = executionCancellerFunc(func(_ context.Context, req pluginapi.CancelExecutionRequest) error {
		cancelRequests <- req
		return nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	req := coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.RequestIDMetadataKey:        "request-cancel-nonstream",
		coreexecutor.ExecutionSessionMetadataKey: "session-cancel-nonstream",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	errResult := make(chan error, 1)
	go func() {
		_, errExecute := adapter.Execute(ctx, auth, req, opts)
		errResult <- errExecute
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("executor did not start")
	}
	cancel()
	select {
	case errExecute := <-errResult:
		if errExecute != context.Canceled {
			t.Fatalf("Execute() error = %v, want context.Canceled", errExecute)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute() did not return after cancellation")
	}
	select {
	case cancelReq := <-cancelRequests:
		if cancelReq.RequestID != "request-cancel-nonstream" || cancelReq.ExecutionSessionID != "session-cancel-nonstream" {
			t.Fatalf("cancel request = %#v", cancelReq)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin did not receive non-stream cancellation")
	}
}

func TestExecutorAdapterBridgesSessionAndAuthClose(t *testing.T) {
	host := NewForTest(nil)
	executor := &fakeExecutor{identifier: "plugin-provider"}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"lifecycle-close",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	closeRequests := make(chan pluginapi.CloseExecutionSessionRequest, 3)
	adapter.sessionCloser = executionSessionCloserFunc(func(_ context.Context, req pluginapi.CloseExecutionSessionRequest) error {
		closeRequests <- req
		return nil
	})

	adapter.CloseExecutionSession("session-1")
	adapter.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
	adapter.CloseExecutionSessionsForAuth("auth-1", "index-1")

	got := make([]pluginapi.CloseExecutionSessionRequest, 0, 3)
	for len(got) < 3 {
		select {
		case req := <-closeRequests:
			got = append(got, req)
		case <-time.After(time.Second):
			t.Fatalf("received %d close requests, want 3", len(got))
		}
	}
	if got[0].Scope != pluginapi.ExecutionSessionCloseScopeSession || got[0].ExecutionSessionID != "session-1" {
		t.Fatalf("specific close = %#v", got[0])
	}
	if got[1].Scope != pluginapi.ExecutionSessionCloseScopeProvider || got[1].ExecutionSessionID != "" {
		t.Fatalf("all-session close = %#v", got[1])
	}
	if got[2].Scope != pluginapi.ExecutionSessionCloseScopeAuth || got[2].AuthID != "auth-1" || got[2].AuthIndex != "index-1" {
		t.Fatalf("auth-scoped close = %#v", got[2])
	}
}

func TestHostProbeProviderReadiness(t *testing.T) {
	host := NewForTest(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	executor := &fakeExecutor{identifier: "qoder"}
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:      "qoder-plugin",
		version: "1.2.3",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: executor,
			ProviderReadiness: providerReadinessFunc(func(_ context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
				if req.Provider != "qoder" || req.AuthID != "auth-1" {
					t.Fatalf("readiness request = %#v", req)
				}
				return pluginapi.ReadinessResponse{
					Ready: true,
					Checks: []pluginapi.ReadinessCheck{
						{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady, Version: "1.0.10"},
						{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
					},
				}, nil
			}),
		}},
	})
	setHostSnapshotForTest(host, true, record)
	host.RegisterExecutors(manager, nil)

	resp, errProbe := host.ProbeProviderReadiness(context.Background(), "qoder", pluginapi.ReadinessRequest{AuthID: "auth-1"})
	if errProbe != nil {
		t.Fatalf("ProbeProviderReadiness() error = %v", errProbe)
	}
	if resp.Provider != "qoder" || !resp.Ready || len(resp.Checks) != 5 {
		t.Fatalf("readiness response = %#v", resp)
	}
	if resp.Checks[0].Level != pluginapi.ReadinessLevelPluginInstalled || resp.Checks[0].State != pluginapi.ReadinessStateReady || resp.Checks[0].Version != "1.2.3" {
		t.Fatalf("plugin readiness check = %#v", resp.Checks[0])
	}
	if resp.Checks[4].Level != pluginapi.ReadinessLevelSessionReady || resp.Checks[4].State != pluginapi.ReadinessStateUnknown {
		t.Fatalf("session readiness check = %#v", resp.Checks[4])
	}
}

func TestHostProbeProviderReadinessRequiresExplicitSessionCheck(t *testing.T) {
	host := NewForTest(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	executor := &fakeExecutor{identifier: "qoder"}
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:      "qoder-session-readiness",
		version: "1.2.3",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: executor,
			ProviderReadiness: providerReadinessFunc(func(_ context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
				if req.ExecutionSessionID != "session-stale" {
					t.Fatalf("readiness session = %q, want session-stale", req.ExecutionSessionID)
				}
				return pluginapi.ReadinessResponse{
					Ready: true,
					Checks: []pluginapi.ReadinessCheck{
						{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateNotReady},
					},
				}, nil
			}),
		}},
	})
	setHostSnapshotForTest(host, true, record)
	host.RegisterExecutors(manager, nil)

	resp, errProbe := host.ProbeProviderReadiness(context.Background(), "qoder", pluginapi.ReadinessRequest{
		AuthID:             "auth-1",
		ExecutionSessionID: "session-stale",
	})
	if errProbe != nil {
		t.Fatalf("ProbeProviderReadiness() error = %v", errProbe)
	}
	if resp.Ready || resp.Checks[4].Level != pluginapi.ReadinessLevelSessionReady || resp.Checks[4].State != pluginapi.ReadinessStateNotReady {
		t.Fatalf("session readiness response = %#v", resp)
	}
}

func TestExecutorAdapterReadinessFailsClosedBeforeExecution(t *testing.T) {
	host := NewForTest(nil)
	executeCalls := 0
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executeCalls++
			return pluginapi.ExecutorResponse{}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-gate",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		return pluginapi.ReadinessResponse{
			Ready: false,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateNotReady},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateNotReady},
			},
		}, nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	_, errExecute := adapter.Execute(context.Background(), auth, coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want readiness failure")
	}
	requestScoped, okRequestScoped := errExecute.(interface{ IsRequestScoped() bool })
	if !okRequestScoped || !requestScoped.IsRequestScoped() {
		t.Fatalf("readiness error = %T, want request-scoped", errExecute)
	}
	if executeCalls != 0 {
		t.Fatalf("executor calls = %d, want 0", executeCalls)
	}
}

func TestExecutorAdapterReadinessFailsClosedBeforeStreamAndCount(t *testing.T) {
	host := NewForTest(nil)
	streamCalls := 0
	countCalls := 0
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		executeStream: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			streamCalls++
			return pluginapi.ExecutorStreamResponse{}, nil
		},
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			countCalls++
			return pluginapi.ExecutorResponse{}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-all-gates",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		return pluginapi.ReadinessResponse{
			Ready: false,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateNotReady},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateNotReady},
			},
		}, nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	req := coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	if _, errStream := adapter.ExecuteStream(context.Background(), auth, req, opts); errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want readiness failure")
	}
	if _, errCount := adapter.CountTokens(context.Background(), auth, req, opts); errCount == nil {
		t.Fatal("CountTokens() error = nil, want readiness failure")
	}
	if streamCalls != 0 || countCalls != 0 {
		t.Fatalf("executor calls = stream:%d count:%d, want 0", streamCalls, countCalls)
	}
}

func TestExecutorAdapterPreExecutionReadinessAllowsNewExplicitSession(t *testing.T) {
	host := NewForTest(nil)
	var captured pluginapi.ExecutorRequest
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			captured = req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-new-session",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(_ context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		if req.ExecutionSessionID != "" {
			t.Fatalf("pre-execution readiness session = %q, want empty for new/attach admission", req.ExecutionSessionID)
		}
		return pluginapi.ReadinessResponse{
			Ready: true,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady},
				{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
				{Level: pluginapi.ReadinessLevelSessionReady, State: pluginapi.ReadinessStateUnknown},
			},
		}, nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.ExecutionSessionMetadataKey: "new-session-1",
	}}

	if _, errExecute := adapter.Execute(context.Background(), auth, coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if captured.ExecutionSessionID != "new-session-1" {
		t.Fatalf("executor session = %q, want new-session-1", captured.ExecutionSessionID)
	}
}

func TestExecutorAdapterReadinessAdmissionIsSingleUse(t *testing.T) {
	host := NewForTest(nil)
	readinessCalls := 0
	executeCalls := 0
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executeCalls++
			return pluginapi.ExecutorResponse{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-single-use",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		readinessCalls++
		return pluginapi.ReadinessResponse{
			Ready: true,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady},
				{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
			},
		}, nil
	})
	auth := &coreauth.Auth{ID: "auth-1", Index: "index-1", Provider: "plugin-provider"}
	req := coreexecutor.Request{Model: "model-1", Format: sdktranslator.FormatOpenAI}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.RequestIDMetadataKey: "request-single-use",
	}}

	admittedCtx, errAdmission := adapter.AdmitExecution(context.Background(), auth, req, opts)
	if errAdmission != nil {
		t.Fatalf("AdmitExecution() error = %v", errAdmission)
	}
	if _, errExecute := adapter.Execute(admittedCtx, auth, req, opts); errExecute != nil {
		t.Fatalf("first Execute() error = %v", errExecute)
	}
	if _, errExecute := adapter.Execute(admittedCtx, auth, req, opts); errExecute != nil {
		t.Fatalf("second Execute() error = %v", errExecute)
	}
	if readinessCalls != 2 {
		t.Fatalf("readiness calls = %d, want initial admission plus one probe for reused context", readinessCalls)
	}
	if executeCalls != 2 {
		t.Fatalf("execute calls = %d, want 2", executeCalls)
	}
}

func TestPluginAuthReadinessRotatesWithoutCredentialCooldown(t *testing.T) {
	tests := []struct {
		name string
		run  func(*coreauth.Manager, coreexecutor.Request, coreexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(manager *coreauth.Manager, req coreexecutor.Request, opts coreexecutor.Options) error {
				_, errExecute := manager.Execute(context.Background(), []string{"plugin-provider"}, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(manager *coreauth.Manager, req coreexecutor.Request, opts coreexecutor.Options) error {
				opts.Stream = true
				result, errStream := manager.ExecuteStream(context.Background(), []string{"plugin-provider"}, req, opts)
				if errStream != nil {
					return errStream
				}
				for range result.Chunks {
				}
				return nil
			},
		},
		{
			name: "count",
			run: func(manager *coreauth.Manager, req coreexecutor.Request, opts coreexecutor.Options) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"plugin-provider"}, req, opts)
				return errCount
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := NewForTest(nil)
			readinessAuthIDs := make([]string, 0, 2)
			executedAuthIDs := make([]string, 0, 1)
			selectedAuthIDs := make([]string, 0, 1)
			hook := &executionResultHook{}
			recordExecution := func(req pluginapi.ExecutorRequest) {
				executedAuthIDs = append(executedAuthIDs, req.AuthID)
			}
			executor := &fakeExecutor{
				identifier: "plugin-provider",
				execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					recordExecution(req)
					return pluginapi.ExecutorResponse{Payload: []byte(`{"ok":true}`)}, nil
				},
				executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
					recordExecution(req)
					chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
					chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")}
					close(chunks)
					return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
				},
				countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
					recordExecution(req)
					return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":1}`)}, nil
				},
			}
			adapter := newCurrentExecutorAdapterForTest(
				host,
				"readiness-auth-fallback",
				executor,
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
				[]sdktranslator.Format{sdktranslator.FormatOpenAI},
			)
			adapter.readiness = providerReadinessFunc(func(_ context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
				readinessAuthIDs = append(readinessAuthIDs, req.AuthID)
				authState := pluginapi.ReadinessStateReady
				ready := true
				if req.AuthID == "a-not-ready" {
					authState = pluginapi.ReadinessStateNotReady
					ready = false
				}
				return pluginapi.ReadinessResponse{
					Ready: ready,
					Checks: []pluginapi.ReadinessCheck{
						{Level: pluginapi.ReadinessLevelRunnerInstalled, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelProtocolReady, State: pluginapi.ReadinessStateReady},
						{Level: pluginapi.ReadinessLevelAuthReady, State: authState},
					},
				}, nil
			})

			manager := coreauth.NewManager(nil, nil, hook)
			manager.RegisterExecutor(adapter)
			for _, authID := range []string{"a-not-ready", "b-ready"} {
				if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "plugin-provider", Status: coreauth.StatusActive}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", authID, errRegister)
				}
			}
			if errRun := tt.run(
				manager,
				coreexecutor.Request{Format: sdktranslator.FormatOpenAI},
				coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
					coreexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
						selectedAuthIDs = append(selectedAuthIDs, authID)
					},
				}},
			); errRun != nil {
				t.Fatalf("manager %s error = %v", tt.name, errRun)
			}
			if len(readinessAuthIDs) != 2 || readinessAuthIDs[0] != "a-not-ready" || readinessAuthIDs[1] != "b-ready" {
				t.Fatalf("readiness auths = %#v, want [a-not-ready b-ready]", readinessAuthIDs)
			}
			if len(executedAuthIDs) != 1 || executedAuthIDs[0] != "b-ready" {
				t.Fatalf("executed auths = %#v, want [b-ready]", executedAuthIDs)
			}
			if len(selectedAuthIDs) != 1 || selectedAuthIDs[0] != "b-ready" {
				t.Fatalf("selected-auth callbacks = %#v, want [b-ready]", selectedAuthIDs)
			}
			notReadyAuth, okAuth := manager.GetByID("a-not-ready")
			if !okAuth || notReadyAuth == nil {
				t.Fatal("not-ready auth missing after fallback")
			}
			if notReadyAuth.Failed != 0 || notReadyAuth.Unavailable || !notReadyAuth.NextRetryAfter.IsZero() || notReadyAuth.Status == coreauth.StatusError {
				t.Fatalf("not-ready auth recorded/penalized: failed=%d status=%q unavailable=%t retry=%v", notReadyAuth.Failed, notReadyAuth.Status, notReadyAuth.Unavailable, notReadyAuth.NextRetryAfter)
			}
			for _, bucket := range notReadyAuth.RecentRequestsSnapshot(time.Now()) {
				if bucket.Success != 0 || bucket.Failed != 0 {
					t.Fatalf("not-ready auth recent request bucket = %#v, want empty", bucket)
				}
			}
			if len(hook.results) != 1 || hook.results[0].AuthID != "b-ready" || !hook.results[0].Success {
				t.Fatalf("result hook calls = %#v, want one b-ready success", hook.results)
			}
		})
	}
}

func TestPluginRunnerReadinessStopsCredentialFallbackWithoutCooldown(t *testing.T) {
	host := NewForTest(nil)
	readinessAuthIDs := make([]string, 0, 1)
	executedAuthIDs := make([]string, 0, 1)
	selectedAuthIDs := make([]string, 0, 1)
	runnerReady := false
	hook := &executionResultHook{}
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executedAuthIDs = append(executedAuthIDs, req.AuthID)
			return pluginapi.ExecutorResponse{}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-runner-stop",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(_ context.Context, req pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		readinessAuthIDs = append(readinessAuthIDs, req.AuthID)
		runnerState := pluginapi.ReadinessStateNotReady
		protocolState := pluginapi.ReadinessStateUnknown
		if runnerReady {
			runnerState = pluginapi.ReadinessStateReady
			protocolState = pluginapi.ReadinessStateReady
		}
		return pluginapi.ReadinessResponse{
			Ready: runnerReady,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: runnerState},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: protocolState},
				{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
			},
		}, nil
	})

	selector := coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{})
	defer selector.Stop()
	manager := coreauth.NewManager(nil, selector, hook)
	manager.RegisterExecutor(adapter)
	for _, authID := range []string{"a-first", "b-must-not-probe"} {
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "plugin-provider", Status: coreauth.StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
	}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.ExecutionSessionMetadataKey: "runner-stop-session",
		coreexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthIDs = append(selectedAuthIDs, authID)
		},
	}}
	_, errExecute := manager.Execute(
		context.Background(),
		[]string{"plugin-provider"},
		coreexecutor.Request{Format: sdktranslator.FormatOpenAI},
		opts,
	)
	if errExecute == nil {
		t.Fatal("Manager.Execute() error = nil, want runner readiness failure")
	}
	if len(readinessAuthIDs) != 1 || readinessAuthIDs[0] != "a-first" {
		t.Fatalf("readiness auths = %#v, want only a-first", readinessAuthIDs)
	}
	if len(executedAuthIDs) != 0 {
		t.Fatalf("executed auths = %#v, want none", executedAuthIDs)
	}
	if len(selectedAuthIDs) != 0 || len(hook.results) != 0 {
		t.Fatalf("pre-dispatch callbacks/results = selected:%#v results:%#v, want none", selectedAuthIDs, hook.results)
	}
	firstAuth, okAuth := manager.GetByID("a-first")
	if !okAuth || firstAuth == nil {
		t.Fatal("first auth missing after readiness failure")
	}
	if firstAuth.Failed != 0 || firstAuth.Unavailable || !firstAuth.NextRetryAfter.IsZero() || firstAuth.Status == coreauth.StatusError {
		t.Fatalf("first auth recorded/penalized: failed=%d status=%q unavailable=%t retry=%v", firstAuth.Failed, firstAuth.Status, firstAuth.Unavailable, firstAuth.NextRetryAfter)
	}

	runnerReady = true
	if _, errRetry := manager.Execute(
		context.Background(),
		[]string{"plugin-provider"},
		coreexecutor.Request{Format: sdktranslator.FormatOpenAI},
		opts,
	); errRetry != nil {
		t.Fatalf("Manager.Execute() after readiness recovery error = %v", errRetry)
	}
	if len(executedAuthIDs) != 1 || executedAuthIDs[0] != "b-must-not-probe" {
		t.Fatalf("executed auths after recovery = %#v, want provisional affinity released and b-must-not-probe selected", executedAuthIDs)
	}
}

func TestPluginRunnerReadinessPreservesEstablishedSessionAffinity(t *testing.T) {
	host := NewForTest(nil)
	runnerReady := true
	executedAuthIDs := make([]string, 0, 2)
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executedAuthIDs = append(executedAuthIDs, req.AuthID)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-warm-affinity",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		runnerState := pluginapi.ReadinessStateNotReady
		protocolState := pluginapi.ReadinessStateUnknown
		if runnerReady {
			runnerState = pluginapi.ReadinessStateReady
			protocolState = pluginapi.ReadinessStateReady
		}
		return pluginapi.ReadinessResponse{
			Ready: runnerReady,
			Checks: []pluginapi.ReadinessCheck{
				{Level: pluginapi.ReadinessLevelRunnerInstalled, State: runnerState},
				{Level: pluginapi.ReadinessLevelProtocolReady, State: protocolState},
				{Level: pluginapi.ReadinessLevelAuthReady, State: pluginapi.ReadinessStateReady},
			},
		}, nil
	})

	selector := coreauth.NewSessionAffinitySelector(&coreauth.RoundRobinSelector{})
	defer selector.Stop()
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(adapter)
	for _, authID := range []string{"a-established", "b-cold-fallback"} {
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Provider: "plugin-provider", Status: coreauth.StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
	}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Metadata: map[string]any{
		coreexecutor.ExecutionSessionMetadataKey: "warm-affinity-session",
	}}
	execute := func() error {
		_, errExecute := manager.Execute(
			context.Background(),
			[]string{"plugin-provider"},
			coreexecutor.Request{Format: sdktranslator.FormatOpenAI},
			opts,
		)
		return errExecute
	}

	if errExecute := execute(); errExecute != nil {
		t.Fatalf("initial Execute() error = %v", errExecute)
	}
	runnerReady = false
	if errExecute := execute(); errExecute == nil {
		t.Fatal("Execute() during runner outage error = nil, want readiness failure")
	}
	runnerReady = true
	if errExecute := execute(); errExecute != nil {
		t.Fatalf("Execute() after readiness recovery error = %v", errExecute)
	}
	if len(executedAuthIDs) != 2 || executedAuthIDs[0] != "a-established" || executedAuthIDs[1] != "a-established" {
		t.Fatalf("executed auths = %#v, want established auth preserved across readiness rejection", executedAuthIDs)
	}
}

func TestExecutorAdapterRejectsReadinessProviderMismatch(t *testing.T) {
	host := NewForTest(nil)
	executor := &fakeExecutor{identifier: "plugin-provider"}
	adapter := newCurrentExecutorAdapterForTest(
		host,
		"readiness-provider-mismatch",
		executor,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.readiness = providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
		return pluginapi.ReadinessResponse{Provider: "other-provider", Ready: true}, nil
	})
	if _, errProbe := adapter.ProbeReadiness(context.Background(), pluginapi.ReadinessRequest{Provider: "plugin-provider"}); errProbe == nil {
		t.Fatal("ProbeReadiness() error = nil, want response provider mismatch")
	}
	if _, errProbe := adapter.ProbeReadiness(context.Background(), pluginapi.ReadinessRequest{Provider: "other-provider"}); errProbe == nil {
		t.Fatal("ProbeReadiness() error = nil, want request provider mismatch")
	}
}

func TestHostProbeLegacyProviderReadinessFailsClosed(t *testing.T) {
	host := NewForTest(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	executor := &fakeExecutor{identifier: "legacy-provider"}
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id:      "legacy-plugin",
		version: "0.9.0",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: executor,
		}},
	})
	setHostSnapshotForTest(host, true, record)
	host.RegisterExecutors(manager, nil)

	resp, errProbe := host.ProbeProviderReadiness(context.Background(), "legacy-provider", pluginapi.ReadinessRequest{})
	if errProbe != nil {
		t.Fatalf("ProbeProviderReadiness() error = %v", errProbe)
	}
	if resp.Ready || len(resp.Checks) != 5 || resp.Checks[1].State != pluginapi.ReadinessStateUnsupported {
		t.Fatalf("legacy readiness response = %#v", resp)
	}
}

func TestHostProbeProviderReadinessRejectsNativeExecutorOwner(t *testing.T) {
	host := NewForTest(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	manager.RegisterExecutor(&fakeProviderExecutor{provider: "native-provider"})
	executor := &fakeExecutor{identifier: "native-provider"}
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id: "shadowed-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: executor,
			ProviderReadiness: providerReadinessFunc(func(context.Context, pluginapi.ReadinessRequest) (pluginapi.ReadinessResponse, error) {
				return pluginapi.ReadinessResponse{Provider: "native-provider", Ready: true}, nil
			}),
		}},
	})
	setHostSnapshotForTest(host, true, record)
	host.RegisterExecutors(manager, nil)

	if _, errProbe := host.ProbeProviderReadiness(context.Background(), "native-provider", pluginapi.ReadinessRequest{}); errProbe == nil {
		t.Fatal("ProbeProviderReadiness() error = nil, want native owner rejection")
	}
}

func TestRegisterExecutorsLifecycleReplacementDoesNotDeadlockOrCloseEquivalentAdapter(t *testing.T) {
	host := NewForTest(nil)
	manager := coreauth.NewManager(nil, nil, nil)
	closeRequests := make(chan pluginapi.CloseExecutionSessionRequest, 2)
	closer := executionSessionCloserFunc(func(_ context.Context, req pluginapi.CloseExecutionSessionRequest) error {
		closeRequests <- req
		return nil
	})
	executor := &fakeExecutor{identifier: "lifecycle-provider"}
	recordV1 := normalizeTestCapabilityRecord(capabilityRecord{
		id:      "lifecycle-provider-plugin",
		version: "1.0.0",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor:               executor,
			ExecutionSessionCloser: closer,
			ExecutorInputFormats:   []string{sdktranslator.FormatOpenAI.String()},
			ExecutorOutputFormats:  []string{sdktranslator.FormatOpenAI.String()},
		}},
	})
	setHostSnapshotForTest(host, true, recordV1)
	host.RegisterExecutors(manager, nil)
	host.RegisterExecutors(manager, nil)
	select {
	case req := <-closeRequests:
		t.Fatalf("equivalent adapter refresh closed sessions: %#v", req)
	default:
	}

	recordV2 := recordV1
	recordV2.version = "2.0.0"
	recordV2.path = "testdata/lifecycle-provider-plugin-v2.plugin"
	setHostSnapshotForTest(host, true, recordV2)
	done := make(chan struct{})
	go func() {
		host.RegisterExecutors(manager, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RegisterExecutors deadlocked while closing replaced adapter")
	}
	select {
	case req := <-closeRequests:
		if req.Scope != pluginapi.ExecutionSessionCloseScopeProvider || req.Provider != "lifecycle-provider" {
			t.Fatalf("replacement close request = %#v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("replaced adapter did not receive all-session close")
	}
}
