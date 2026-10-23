package cliproxy

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type retryModelProvider struct {
	mu       sync.Mutex
	failures int
	err      error
	calls    int
	empty    bool
}

func (p *retryModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{}, nil
}

func (p *retryModelProvider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.failures > 0 {
		p.failures--
		return pluginapi.ModelResponse{}, p.err
	}
	if p.empty {
		return pluginapi.ModelResponse{Provider: "qoder"}, nil
	}
	return pluginapi.ModelResponse{Provider: "qoder", Models: []pluginapi.ModelInfo{{ID: "qmodel_retry", DisplayName: "Retry"}}}, nil
}

func newRetryTestService(t *testing.T, models pluginapi.ModelProvider) (*Service, *coreauth.Auth) {
	t.Helper()
	auth := &coreauth.Auth{ID: "qoder-plugin-model-retry-test", FileName: "qoder-plugin-model-retry-test.json", Provider: "qoder", Status: coreauth.StatusActive}
	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager := coreauth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	host := pluginhost.New()
	host.RegisterPluginForTest("qoder", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		AuthProvider:  refreshTestAuthProvider{},
		ModelProvider: models,
	}})
	service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: host}
	return service, auth
}

type staticPluginError struct {
	code   string
	status int
}

func (e staticPluginError) Error() string      { return "plugin discovery failed" }
func (e staticPluginError) PluginCode() string { return e.code }
func (e staticPluginError) StatusCode() int    { return e.status }

func TestPluginModelDiscoveryRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context_cancelled", context.Canceled, false},
		{"plain_network", errors.New("catalog unavailable"), true},
		{"http_503", staticPluginError{code: "catalog_upstream_error", status: 503}, true},
		{"http_429", staticPluginError{code: "catalog_upstream_error", status: 429}, true},
		{"http_401", staticPluginError{code: "catalog_upstream_error", status: 401}, false},
		{"http_403", staticPluginError{code: "catalog_upstream_error", status: 403}, false},
		{"code_invalid_auth", staticPluginError{code: "invalid_auth", status: 400}, false},
		{"code_catalog_rejected", staticPluginError{code: "catalog_rejected", status: 502}, false},
		{"code_catalog_empty", staticPluginError{code: "catalog_empty", status: 503}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluginModelDiscoveryRetryable(tc.err); got != tc.want {
				t.Fatalf("pluginModelDiscoveryRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestPluginModelRetrySchedulesOnRetryableFailureAndClearsOnSuccess(t *testing.T) {
	provider := &retryModelProvider{failures: 1, err: errors.New("catalog unavailable")}
	service, auth := newRetryTestService(t, provider)
	retry := newPluginModelRegistrationRetry(service)
	service.pluginModelRetry = retry
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retry.start(ctx)
	t.Cleanup(retry.stop)

	if _, err := service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil); err == nil {
		t.Fatal("retryable discovery failure was hidden")
	}
	retry.mu.Lock()
	pending, hasPending := retry.pending[auth.ID]
	retry.mu.Unlock()
	if !hasPending {
		t.Fatal("retryable discovery failure was not scheduled")
	}
	if wait := time.Until(pending); wait <= 0 || wait > pluginModelRegistrationRetryInterval {
		t.Fatalf("scheduled retry at %v, want within %s", pending, pluginModelRegistrationRetryInterval)
	}

	// 执行自动重试使用的注册路径，同时核对 registry 和待重试状态。
	retry.retryOne(ctx, auth.ID)
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) == 0 {
		t.Fatal("retry did not register models after recovery")
	}
	retry.mu.Lock()
	stillPending := len(retry.pending)
	retry.mu.Unlock()
	if stillPending != 0 {
		t.Fatalf("successful registration left %d pending retries", stillPending)
	}
}

func TestPluginModelRetryClearsPendingAfterTerminalFailure(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			provider := &retryModelProvider{failures: 1, err: staticPluginError{code: "catalog_upstream_error", status: 503}}
			service, auth := newRetryTestService(t, provider)
			retry := newPluginModelRegistrationRetry(service)
			service.pluginModelRetry = retry
			retry.start(context.Background())
			defer retry.stop()
			if _, err := service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil); err == nil {
				t.Fatal("expected temporary discovery failure")
			}
			provider.mu.Lock()
			provider.failures = 1
			provider.err = staticPluginError{code: "catalog_auth_rejected", status: status}
			provider.mu.Unlock()
			if _, err := service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil); err == nil {
				t.Fatal("expected credential rejection")
			}
			retry.mu.Lock()
			defer retry.mu.Unlock()
			if len(retry.pending) != 0 || retry.timer != nil {
				t.Fatal("terminal rejection retained a scheduled retry")
			}
		})
	}
}

func TestPluginModelRetryClearsPendingAfterEmptySuccess(t *testing.T) {
	provider := &retryModelProvider{failures: 1, err: errors.New("temporary failure"), empty: true}
	service, auth := newRetryTestService(t, provider)
	retry := newPluginModelRegistrationRetry(service)
	service.pluginModelRetry = retry
	retry.start(context.Background())
	defer retry.stop()
	_, _ = service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil)
	if _, err := service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil); err != nil {
		t.Fatal(err)
	}
	retry.mu.Lock()
	defer retry.mu.Unlock()
	if len(retry.pending) != 0 {
		t.Fatal("successful empty discovery retained a scheduled retry")
	}
}

func TestPluginModelRetryPreservesPendingOnCallerCancellation(t *testing.T) {
	service, auth := newRetryTestService(t, &retryModelProvider{})
	retry := newPluginModelRegistrationRetry(service)
	service.pluginModelRetry = retry
	retry.start(context.Background())
	defer retry.stop()
	service.notePluginModelDiscoveryFailure(auth, errors.New("temporary failure"))
	service.notePluginModelDiscoveryFailure(auth, context.Canceled)
	retry.mu.Lock()
	defer retry.mu.Unlock()
	if len(retry.pending) != 1 {
		t.Fatal("caller cancellation erased the pending recovery attempt")
	}
}

func TestPluginModelRetryDoesNotScheduleCredentialRejection(t *testing.T) {
	provider := &retryModelProvider{failures: 1, err: staticPluginError{code: "catalog_upstream_error", status: 401}}
	service, auth := newRetryTestService(t, provider)
	retry := newPluginModelRegistrationRetry(service)
	service.pluginModelRetry = retry
	retry.start(context.Background())
	defer retry.stop()

	if _, err := service.tryRegisterPluginModelsForAuth(context.Background(), auth, "qoder", "oauth", nil); err == nil {
		t.Fatal("credential rejection was hidden")
	}
	retry.mu.Lock()
	pending := len(retry.pending)
	retry.mu.Unlock()
	if pending != 0 {
		t.Fatalf("credential rejection scheduled %d automatic retries, want 0", pending)
	}
}

func TestPluginModelRetrySkipsDisabledAndMissingAuths(t *testing.T) {
	provider := &retryModelProvider{}
	service, auth := newRetryTestService(t, provider)
	retry := newPluginModelRegistrationRetry(service)
	service.pluginModelRetry = retry
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retry.start(ctx)
	defer retry.stop()
	auth.Disabled = true
	if _, err := service.coreManager.Update(ctx, auth); err != nil {
		t.Fatal(err)
	}
	retry.retryOne(ctx, auth.ID)
	retry.retryOne(ctx, "auth-that-does-not-exist")
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 0 {
		t.Fatal("disabled or missing auth reached plugin discovery")
	}
}
