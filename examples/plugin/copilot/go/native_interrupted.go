package main

import (
	"crypto/sha256"
	"slices"
	"time"
)

// 失败流只留下已公开的工具身份摘要，不保存密文、原始参数或工具结果。
type toolFingerprint struct{ id, name, args [sha256.Size]byte }
type interruptedToolBatch struct {
	identity executionIdentity
	model    string
	tools    []toolFingerprint
	complete bool
	created  time.Time
}

func (t nativeToolIdentity) fingerprint() toolFingerprint {
	hash := func(s string) [sha256.Size]byte {
		if s == "" {
			return [sha256.Size]byte{}
		}
		return sha256.Sum256([]byte(s))
	}
	return toolFingerprint{hash(t.id), hash(t.name), hash(t.arguments)}
}

func (r *pluginRuntime) interruptNativeToolsLocked(e *nativeExecution) {
	if len(e.tools) == 0 || e.replayClosed || !r.accepting || r.generation != e.generation {
		return
	}
	b := interruptedToolBatch{identity: e.identity, model: e.model, complete: e.toolsComplete, created: time.Now()}
	for _, t := range e.tools {
		b.tools = append(b.tools, t.fingerprint())
	}
	r.interruptedTools = slices.DeleteFunc(r.interruptedTools, func(old interruptedToolBatch) bool { return b.created.Sub(old.created) > nativeReasoningTTL })
	if len(r.interruptedTools) >= nativeReasoningMaxEntries {
		r.interruptedTools = slices.Delete(r.interruptedTools, 0, 1)
	}
	r.interruptedTools = append(r.interruptedTools, b)
}

func (r *pluginRuntime) interruptedNativeBatchLocked(exec *nativeExecution, model string, batch map[string]nativeToolIdentity, valid bool) bool {
	var want map[[sha256.Size]byte]toolFingerprint
	now := time.Now()
	for _, failed := range r.interruptedTools {
		if failed.identity.key != exec.identity.key || failed.model != model || now.Sub(failed.created) > nativeReasoningTTL || (valid && failed.complete && len(failed.tools) != len(batch)) {
			continue
		}
		if want == nil {
			want = make(map[[sha256.Size]byte]toolFingerprint, len(batch))
			for _, call := range batch {
				f := call.fingerprint()
				want[f.id] = f
			}
		}
		overlap, possible := false, true
		for _, f := range failed.tools {
			w, ok := want[f.id]
			if !ok {
				if valid {
					possible = false
					break
				}
				continue
			}
			var zero [sha256.Size]byte
			if (f.name != zero && w.name != zero && f.name != w.name) || (f.args != zero && w.args != zero && f.args != w.args) {
				possible = false
				break
			}
			overlap = true
		}
		if overlap && possible {
			return true
		}
	}
	return false
}
