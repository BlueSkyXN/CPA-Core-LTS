package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedFixtureBody struct {
	ctx                 context.Context
	started, closed     chan struct{}
	readOnce, closeOnce sync.Once
}

func (b *blockedFixtureBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *blockedFixtureBody) Close() error { b.closeOnce.Do(func() { close(b.closed) }); return nil }
func TestV2BlockedBodyCancelAndBoundedDetach(t *testing.T) {
	for _, phase := range []string{"handshake-block", "model-block"} {
		for _, action := range []string{"cancel", "unload"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				if isolateDynamic(t) {
					return
				}
				f := newV2Fixture(t, true)
				b := &blockedFixtureBody{started: make(chan struct{}), closed: make(chan struct{})}
				f.transport.blocked = b
				f.transport.mode = phase
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				go func() {
					defer close(done)
					req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model))).WithContext(ctx)
					req.Header.Set("Content-Type", "application/json")
					f.router.ServeHTTP(httptest.NewRecorder(), req)
				}()
				select {
				case <-b.started:
				case <-time.After(3 * time.Second):
					t.Fatal("body read not started")
				}
				if action == "cancel" {
					cancel()
				} else {
					detachCtx, stopDetach := context.WithTimeout(context.Background(), 50*time.Millisecond)
					defer stopDetach()
					if !f.host.UnloadPluginContext(detachCtx, "zcode-coding-plan") {
						t.Fatal("detach failed")
					}
					// 宿主先摘除再 drain；有界卸载不会替调用方取消在途 RPC。
					select {
					case <-done:
						t.Fatal("detach unexpectedly completed blocked request")
					default:
					}
					cancel()
				}
				select {
				case <-b.closed:
				case <-time.After(3 * time.Second):
					t.Fatal("upstream body not closed")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("HTTP execution not released")
				}
				f.checkUsage(t, true)
			})
		}
	}
}
func TestV2Redirect308StopsBeforeReplay(t *testing.T) {
	for _, phase := range []string{"handshake-redirect", "model-redirect"} {
		t.Run(phase, func(t *testing.T) {
			if isolateDynamic(t) {
				return
			}
			f := newV2Fixture(t, true)
			f.transport.mode = phase
			f.transport.redirectStatus = 308
			res := f.post(t, fmt.Sprintf(`{"model":%q,"input":"hello"}`, v2Model), "")
			if res.Code < 400 || f.transport.targets != 0 || len(f.transport.captured) > 1 {
				t.Fatal("redirect replay not blocked")
			}
			f.checkUsage(t, true)
		})
	}
}
