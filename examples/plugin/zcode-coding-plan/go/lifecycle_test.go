package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestSSETerminalIgnoresLateFrames(t *testing.T) {
	var out bytes.Buffer
	p := sseParser{emit: func(b []byte) error { out.Write(b); return nil }}
	terminal := []byte("data: {\"type\":\"message_stop\"}\n\n")
	if err := p.feed(append(bytes.Clone(terminal), []byte("data: invalid\n\n")...)); err != nil {
		t.Fatal(err)
	}
	if err := p.feed([]byte("data: invalid\n\n")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), terminal) {
		t.Fatal("more than one terminal emitted")
	}
}

type blockingHandshakeHost struct {
	read, closed chan struct{}
	once         sync.Once
}

func (h *blockingHandshakeHost) Call(method string, value any) (json.RawMessage, error) {
	switch method {
	case "host.http.do_stream":
		return encode(httpStream{StatusCode: 200, StreamID: "handshake"}), nil
	case "host.http.stream_read":
		close(h.read)
		<-h.closed
		return encode(httpChunk{Done: true}), nil
	case "host.http.stream_close":
		h.once.Do(func() { close(h.closed) })
		return []byte(`{}`), nil
	}
	return nil, problem(500, "unexpected", "Unexpected callback")
}
func TestCancelAndQuiesceCloseHandshakeBody(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "quiesce"}[stop], func(t *testing.T) {
			h := &blockingHandshakeHost{read: make(chan struct{}), closed: make(chan struct{})}
			r := newRuntime(h)
			req := executorRequest{RequestID: "synthetic", CallbackID: "callback"}
			e, err := r.admit(req, &config{MaxInflight: 1})
			if err != nil {
				t.Fatal(err)
			}
			s := &signer{apiKey: "synthetic-key.synthetic-secret", endpoint: "https://open.bigmodel.cn/api/anthropic/v1/messages"}
			done := make(chan error, 1)
			go func() { _, err := s.handshake(r, e); r.finish(e); done <- err }()
			select {
			case <-h.read:
			case <-time.After(time.Second):
				t.Fatal("handshake never read")
			}
			if stop {
				r.stop()
			} else {
				r.cancelMatching(cancelRequest{RequestID: req.RequestID})
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled handshake succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("handshake not cancelled")
			}
			if e.ctx.Err() != context.Canceled {
				t.Fatal("execution not cancelled")
			}
		})
	}
}
