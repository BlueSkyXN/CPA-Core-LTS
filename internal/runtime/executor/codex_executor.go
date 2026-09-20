package executor

import (
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// CodexExecutor handles Codex requests with an instance-scoped OAuth affinity index.
type CodexExecutor struct {
	cfg                *config.Config
	affinity           *codexAffinityStore
	affinityGeneration uint64
	affinityOnce       sync.Once
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor {
	return &CodexExecutor{cfg: cfg, affinity: newCodexAffinityStore()}
}

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) modelLevelCooling() bool {
	return e != nil && e.cfg != nil && e.cfg.Codex.ModelLevelCooling
}

func (e *CodexExecutor) affinityStore() *codexAffinityStore {
	e.affinityOnce.Do(func() {
		if e.affinity == nil {
			e.affinity = newCodexAffinityStore()
		}
	})
	return e.affinity
}
