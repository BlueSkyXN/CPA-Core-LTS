package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type sseParser struct {
	pending  []byte
	terminal bool
	emit     func([]byte) error
}

func separator(b []byte) (int, int) {
	for i := 0; i < len(b); i++ {
		if b[i] == '\r' && i+3 < len(b) && bytes.Equal(b[i:i+4], []byte("\r\n\r\n")) {
			return i, 4
		}
		if i+1 < len(b) && ((b[i] == '\n' && b[i+1] == '\n') || (b[i] == '\r' && b[i+1] == '\r')) {
			return i, 2
		}
	}
	return -1, 0
}
func (p *sseParser) feed(b []byte) error {
	if p.terminal {
		return nil
	}
	p.pending = append(p.pending, b...)
	for {
		index, n := separator(p.pending)
		if index < 0 {
			break
		}
		if index > maxEvent {
			return problem(502, "response_too_large", "SSE event exceeds size limit")
		}
		wire := bytes.Clone(p.pending[:index+n])
		p.pending = p.pending[index+n:]
		if err := p.frame(wire[:index]); err != nil {
			return err
		}
		if err := p.emit(wire); err != nil {
			return err
		}
		// message_stop 已确定终态，不等待 EOF，也不把迟到帧变成第二个结果。
		if p.terminal {
			p.pending = nil
			return nil
		}
	}
	if len(p.pending) > maxEvent {
		return problem(502, "response_too_large", "SSE event exceeds size limit")
	}
	return nil
}
func (p *sseParser) frame(b []byte) error {
	if !utf8.Valid(b) {
		return problem(502, "invalid_response", "Invalid SSE text")
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	data := []string{}
	event := ""
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimPrefix(line[6:], " ")
		}
	}
	text := strings.Join(data, "\n")
	if text == "" {
		return nil
	}
	if text == "[DONE]" {
		if !p.terminal {
			return problem(502, "truncated_stream", "Stream ended without message_stop")
		}
		return nil
	}
	var value map[string]any
	if json.Unmarshal([]byte(text), &value) != nil {
		return problem(502, "invalid_response", "Invalid SSE JSON")
	}
	if value["type"] == "error" || event == "error" {
		return problem(502, "upstream_error", "Upstream reported a stream error")
	}
	if p.terminal && value["type"] != "ping" {
		return problem(502, "invalid_response", "Data after terminal event")
	}
	if value["type"] == "message_stop" {
		p.terminal = true
	}
	return nil
}
func (p *sseParser) finish() error {
	if len(bytes.TrimSpace(p.pending)) > 0 {
		if err := p.frame(p.pending); err != nil {
			return err
		}
		if err := p.emit(append(bytes.Clone(p.pending), []byte("\n\n")...)); err != nil {
			return err
		}
	}
	p.pending = nil
	if !p.terminal {
		return problem(502, "truncated_stream", "Upstream closed before message_stop")
	}
	return nil
}
