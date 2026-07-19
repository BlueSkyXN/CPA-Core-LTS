package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type retryWithoutPenaltyHedgeRequestState struct {
	secondLaneDisabled bool
}

type retryWithoutPenaltyHedgeOutcome struct {
	response          cliproxyexecutor.Response
	stream            *cliproxyexecutor.StreamResult
	err               error
	attempts          int
	disableSecondLane bool
	usageAccounted    bool
}

type retryWithoutPenaltyHedgeLaneResult struct {
	name           string
	authID         string
	response       cliproxyexecutor.Response
	stream         *cliproxyexecutor.StreamResult
	streamHeaders  http.Header
	streamChunks   []cliproxyexecutor.StreamChunk
	streamMetadata map[string]any
	err            error
	dispatched     bool
	usageAccounted bool
}

type retryWithoutPenaltyStreamBufferLimitError struct {
	limit int64
}

func (e retryWithoutPenaltyStreamBufferLimitError) Error() string {
	return fmt.Sprintf("retry without penalty quality stream buffer limit exceeded: %d bytes", e.limit)
}

func (retryWithoutPenaltyStreamBufferLimitError) StatusCode() int {
	return http.StatusBadGateway
}

func (retryWithoutPenaltyStreamBufferLimitError) IsRequestScoped() bool {
	return true
}

type retryWithoutPenaltyHedgeLaneHandle struct {
	name   string
	cancel context.CancelFunc
}

type retryWithoutPenaltyHedgeLaneTracker struct {
	mu           sync.Mutex
	selected     bool
	authID       string
	selectedCh   chan struct{}
	selectedOnce sync.Once
}

func newRetryWithoutPenaltyHedgeLaneTracker() *retryWithoutPenaltyHedgeLaneTracker {
	return &retryWithoutPenaltyHedgeLaneTracker{
		selectedCh: make(chan struct{}),
	}
}

func (t *retryWithoutPenaltyHedgeLaneTracker) selectedCallback() func(string) {
	return func(authID string) {
		t.markSelected(authID)
	}
}

func (t *retryWithoutPenaltyHedgeLaneTracker) markSelected(authID string) {
	authID = strings.TrimSpace(authID)
	if t == nil || authID == "" {
		return
	}
	t.mu.Lock()
	t.selected = true
	t.authID = authID
	t.mu.Unlock()
	t.selectedOnce.Do(func() {
		close(t.selectedCh)
	})
}

func (t *retryWithoutPenaltyHedgeLaneTracker) dispatched() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.selected
}

func (t *retryWithoutPenaltyHedgeLaneTracker) selectedAuthID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.authID
}

func (t *retryWithoutPenaltyHedgeLaneTracker) selectedNotify() <-chan struct{} {
	if t == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return t.selectedCh
}

type retryWithoutPenaltyHedgeLaneCoordinator struct {
	mu       sync.Mutex
	claims   map[string]string
	laneAuth map[string]string
}

func newRetryWithoutPenaltyHedgeLaneCoordinator() *retryWithoutPenaltyHedgeLaneCoordinator {
	return &retryWithoutPenaltyHedgeLaneCoordinator{
		claims:   make(map[string]string),
		laneAuth: make(map[string]string),
	}
}

func (c *retryWithoutPenaltyHedgeLaneCoordinator) claim(laneName, authID string) bool {
	if c == nil {
		return true
	}
	laneName = strings.TrimSpace(laneName)
	authID = strings.TrimSpace(authID)
	if laneName == "" || authID == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.laneAuth[laneName]; previous != "" && previous != authID {
		delete(c.claims, previous)
		delete(c.laneAuth, laneName)
	}
	if owner := c.claims[authID]; owner != "" && owner != laneName {
		return false
	}
	c.claims[authID] = laneName
	c.laneAuth[laneName] = authID
	return true
}

func (c *retryWithoutPenaltyHedgeLaneCoordinator) release(laneName string) {
	if c == nil {
		return
	}
	laneName = strings.TrimSpace(laneName)
	if laneName == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if authID := c.laneAuth[laneName]; authID != "" {
		delete(c.claims, authID)
	}
	delete(c.laneAuth, laneName)
}

func (m *Manager) executeRetryWithoutPenaltyHedged(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) retryWithoutPenaltyHedgeOutcome {
	if remainingRetries <= 0 {
		return retryWithoutPenaltyHedgeOutcome{}
	}
	if policy.mode == retryWithoutPenaltyHedgeModeQuality {
		return m.executeRetryWithoutPenaltyHedgedQuality(ctx, providers, req, opts, maxRetryCredentials, class, policy, remainingRetries, accumulator, state, fallbackCandidate)
	}
	attempts := 0
	reservedAttempts := 0
	usageAccounted := false
	disableSecondLane := false
	var lastAbnormalErr error
	var lastAbnormalAuthID string
	var lastZeroDispatchErr error
	selectedCallback := retryWithoutPenaltyHedgeSelectedAuthCallback(opts)
	var coordinator *retryWithoutPenaltyHedgeLaneCoordinator
	if policy.requireDistinctAuth {
		coordinator = newRetryWithoutPenaltyHedgeLaneCoordinator()
	}

	startLane := func(resultCh chan<- retryWithoutPenaltyHedgeLaneResult, handles map[string]retryWithoutPenaltyHedgeLaneHandle, pending *int, name string, excludeAuthIDs []string, onAcceptedAuth func(string)) *retryWithoutPenaltyHedgeLaneTracker {
		if reservedAttempts >= remainingRetries {
			return nil
		}
		laneCtx, cancel := context.WithCancel(ctx)
		tracker := newRetryWithoutPenaltyHedgeLaneTracker()
		laneSelectedCallback := tracker.selectedCallback()
		if coordinator != nil {
			laneSelectedCallback = func(authID string) {
				if !coordinator.claim(name, authID) {
					cancel()
					return
				}
				tracker.markSelected(authID)
				if onAcceptedAuth != nil {
					onAcceptedAuth(authID)
				}
			}
		}
		laneOpts := retryWithoutPenaltyHedgeOptions(opts, accumulator, excludeAuthIDs, laneSelectedCallback)
		laneReq := cloneRetryWithoutPenaltyHedgeRequest(req)
		handles[name] = retryWithoutPenaltyHedgeLaneHandle{name: name, cancel: cancel}
		(*pending)++
		reservedAttempts++
		go func() {
			defer coordinator.release(name)
			resp, err := m.executeMixedOnce(laneCtx, providers, laneReq, laneOpts, maxRetryCredentials)
			dispatched := tracker.dispatched()
			accounted := false
			if err != nil && accumulator != nil {
				if detail, ok := retryWithoutPenaltyUsageDetail(err); ok {
					accumulator.Add(detail)
					accounted = true
				}
			}
			resultCh <- retryWithoutPenaltyHedgeLaneResult{
				name:           name,
				authID:         tracker.selectedAuthID(),
				response:       resp,
				err:            err,
				dispatched:     dispatched,
				usageAccounted: accounted,
			}
		}()
		return tracker
	}

	cancelLosers := func(handles map[string]retryWithoutPenaltyHedgeLaneHandle, winner string) {
		for name, handle := range handles {
			if name != winner && handle.cancel != nil {
				handle.cancel()
			}
		}
	}

	for attempts < remainingRetries {
		resultCh := make(chan retryWithoutPenaltyHedgeLaneResult, 2)
		handles := make(map[string]retryWithoutPenaltyHedgeLaneHandle, 2)
		pending := 0
		waveRemaining := remainingRetries - attempts
		waveHadAbnormal := false
		var waveOrdinaryErr error

		primaryTracker := startLane(resultCh, handles, &pending, "primary", nil, nil)
		if primaryTracker == nil {
			break
		}
		secondAllowed := !disableSecondLane && retryWithoutPenaltySecondLaneAllowed(policy, waveRemaining, state)
		var timer *time.Timer
		var timerC <-chan time.Time
		if secondAllowed {
			timer = time.NewTimer(policy.hedgeDelay)
			timerC = timer.C
		}

		for pending > 0 {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				cancelLosers(handles, "")
				return retryWithoutPenaltyHedgeOutcome{err: ctx.Err(), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			case res := <-resultCh:
				pending--
				if out, done := processRetryWithoutPenaltyExecuteHedgeResult(res, &attempts, &reservedAttempts, &usageAccounted, &disableSecondLane, &waveHadAbnormal, &waveOrdinaryErr, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr, fallbackCandidate); done {
					if timer != nil {
						timer.Stop()
					}
					cancelLosers(handles, res.name)
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, res.authID)
					out.attempts = attempts
					out.disableSecondLane = disableSecondLane
					out.usageAccounted = usageAccounted
					return out
				}
			case <-timerC:
				timerC = nil
				if pending <= 0 || reservedAttempts >= remainingRetries {
					continue
				}
				// Keep hedging delay-bound: if primary has not selected an auth yet,
				// exclude the triggering auth and still start the secondary lane.
				exclude := retryWithoutPenaltySecondLaneExcludes(policy, primaryTracker.selectedAuthID())
				primaryCancel := handles["primary"].cancel
				startLane(resultCh, handles, &pending, "secondary", exclude, func(string) {
					if policy.requireDistinctAuth && primaryTracker.selectedAuthID() == "" && primaryCancel != nil {
						primaryCancel()
					}
				})
			}
		}
		if timer != nil {
			timer.Stop()
		}
		if waveHadAbnormal {
			if attempts >= remainingRetries {
				out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				}
				return out
			}
			continue
		}
		if waveOrdinaryErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: waveOrdinaryErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
		if lastZeroDispatchErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
	}

	if lastAbnormalErr != nil {
		out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
		}
		return out
	}
	err := lastZeroDispatchErr
	return retryWithoutPenaltyHedgeOutcome{err: err, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func (m *Manager) executeStreamRetryWithoutPenaltyHedged(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) retryWithoutPenaltyHedgeOutcome {
	if remainingRetries <= 0 {
		return retryWithoutPenaltyHedgeOutcome{}
	}
	if policy.mode == retryWithoutPenaltyHedgeModeQuality {
		return m.executeStreamRetryWithoutPenaltyHedgedQuality(ctx, providers, req, opts, maxRetryCredentials, class, policy, remainingRetries, accumulator, state, fallbackCandidate)
	}
	attempts := 0
	reservedAttempts := 0
	usageAccounted := false
	disableSecondLane := false
	var lastAbnormalErr error
	var lastAbnormalAuthID string
	var lastZeroDispatchErr error
	selectedCallback := retryWithoutPenaltyHedgeSelectedAuthCallback(opts)
	var coordinator *retryWithoutPenaltyHedgeLaneCoordinator
	if policy.requireDistinctAuth {
		coordinator = newRetryWithoutPenaltyHedgeLaneCoordinator()
	}

	startLane := func(resultCh chan<- retryWithoutPenaltyHedgeLaneResult, handles map[string]retryWithoutPenaltyHedgeLaneHandle, pending *int, name string, excludeAuthIDs []string, onAcceptedAuth func(string)) *retryWithoutPenaltyHedgeLaneTracker {
		if reservedAttempts >= remainingRetries {
			return nil
		}
		laneCtx, cancel := context.WithCancel(ctx)
		tracker := newRetryWithoutPenaltyHedgeLaneTracker()
		laneSelectedCallback := tracker.selectedCallback()
		if coordinator != nil {
			laneSelectedCallback = func(authID string) {
				if !coordinator.claim(name, authID) {
					cancel()
					return
				}
				tracker.markSelected(authID)
				if onAcceptedAuth != nil {
					onAcceptedAuth(authID)
				}
			}
		}
		laneOpts := retryWithoutPenaltyHedgeOptions(opts, accumulator, excludeAuthIDs, laneSelectedCallback)
		laneReq := cloneRetryWithoutPenaltyHedgeRequest(req)
		handles[name] = retryWithoutPenaltyHedgeLaneHandle{name: name, cancel: cancel}
		(*pending)++
		reservedAttempts++
		go func() {
			defer coordinator.release(name)
			stream, err := m.executeStreamMixedOnce(laneCtx, providers, laneReq, laneOpts, maxRetryCredentials)
			dispatched := tracker.dispatched()
			accounted := false
			if err != nil && accumulator != nil {
				if detail, ok := retryWithoutPenaltyUsageDetail(err); ok {
					accumulator.Add(detail)
					accounted = true
				}
			}
			resultCh <- retryWithoutPenaltyHedgeLaneResult{
				name:           name,
				authID:         tracker.selectedAuthID(),
				stream:         stream,
				err:            err,
				dispatched:     dispatched,
				usageAccounted: accounted,
			}
		}()
		return tracker
	}

	cancelLosers := func(handles map[string]retryWithoutPenaltyHedgeLaneHandle, winner string) {
		for name, handle := range handles {
			if name != winner && handle.cancel != nil {
				handle.cancel()
			}
		}
	}

	for attempts < remainingRetries {
		resultCh := make(chan retryWithoutPenaltyHedgeLaneResult, 2)
		handles := make(map[string]retryWithoutPenaltyHedgeLaneHandle, 2)
		pending := 0
		waveRemaining := remainingRetries - attempts
		waveHadAbnormal := false
		var waveOrdinaryErr error

		primaryTracker := startLane(resultCh, handles, &pending, "primary", nil, nil)
		if primaryTracker == nil {
			break
		}
		secondAllowed := !disableSecondLane && retryWithoutPenaltySecondLaneAllowed(policy, waveRemaining, state)
		var timer *time.Timer
		var timerC <-chan time.Time
		if secondAllowed {
			timer = time.NewTimer(policy.hedgeDelay)
			timerC = timer.C
		}

		for pending > 0 {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				cancelLosers(handles, "")
				return retryWithoutPenaltyHedgeOutcome{err: ctx.Err(), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			case res := <-resultCh:
				pending--
				if out, done := processRetryWithoutPenaltyStreamHedgeResult(res, handles[res.name].cancel, &attempts, &reservedAttempts, &usageAccounted, &disableSecondLane, &waveHadAbnormal, &waveOrdinaryErr, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr, fallbackCandidate); done {
					if timer != nil {
						timer.Stop()
					}
					cancelLosers(handles, res.name)
					drainRetryWithoutPenaltyStreamHedgeResults(resultCh, pending)
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, res.authID)
					out.attempts = attempts
					out.disableSecondLane = disableSecondLane
					out.usageAccounted = usageAccounted
					return out
				}
			case <-timerC:
				timerC = nil
				if pending <= 0 || reservedAttempts >= remainingRetries {
					continue
				}
				// Keep hedging delay-bound: if primary has not selected an auth yet,
				// exclude the triggering auth and still start the secondary lane.
				exclude := retryWithoutPenaltySecondLaneExcludes(policy, primaryTracker.selectedAuthID())
				primaryCancel := handles["primary"].cancel
				startLane(resultCh, handles, &pending, "secondary", exclude, func(string) {
					if policy.requireDistinctAuth && primaryTracker.selectedAuthID() == "" && primaryCancel != nil {
						primaryCancel()
					}
				})
			}
		}
		if timer != nil {
			timer.Stop()
		}
		if waveHadAbnormal {
			if attempts >= remainingRetries {
				out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				}
				return out
			}
			continue
		}
		if waveOrdinaryErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: waveOrdinaryErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
		if lastZeroDispatchErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
	}
	if lastAbnormalErr != nil {
		out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
		}
		return out
	}
	err := lastZeroDispatchErr
	return retryWithoutPenaltyHedgeOutcome{err: err, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func (m *Manager) executeRetryWithoutPenaltyHedgedQuality(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) retryWithoutPenaltyHedgeOutcome {
	attempts := 0
	reservedAttempts := 0
	usageAccounted := false
	disableSecondLane := false
	var lastAbnormalErr error
	var lastAbnormalAuthID string
	var lastZeroDispatchErr error
	selectedCallback := retryWithoutPenaltyHedgeSelectedAuthCallback(opts)
	var coordinator *retryWithoutPenaltyHedgeLaneCoordinator
	if policy.requireDistinctAuth {
		coordinator = newRetryWithoutPenaltyHedgeLaneCoordinator()
	}

	startLane := func(resultCh chan<- retryWithoutPenaltyHedgeLaneResult, handles map[string]retryWithoutPenaltyHedgeLaneHandle, pending *int, name string, excludeAuthIDs []string, onAcceptedAuth func(string)) *retryWithoutPenaltyHedgeLaneTracker {
		if reservedAttempts >= remainingRetries {
			return nil
		}
		laneCtx, cancel := context.WithCancel(ctx)
		tracker := newRetryWithoutPenaltyHedgeLaneTracker()
		laneSelectedCallback := tracker.selectedCallback()
		if coordinator != nil {
			laneSelectedCallback = func(authID string) {
				if !coordinator.claim(name, authID) {
					cancel()
					return
				}
				tracker.markSelected(authID)
				if onAcceptedAuth != nil {
					onAcceptedAuth(authID)
				}
			}
		}
		laneOpts := retryWithoutPenaltyHedgeOptions(opts, accumulator, excludeAuthIDs, laneSelectedCallback)
		laneReq := cloneRetryWithoutPenaltyHedgeRequest(req)
		handles[name] = retryWithoutPenaltyHedgeLaneHandle{name: name, cancel: cancel}
		(*pending)++
		reservedAttempts++
		go func() {
			defer coordinator.release(name)
			resp, err := m.executeMixedOnce(laneCtx, providers, laneReq, laneOpts, maxRetryCredentials)
			dispatched := tracker.dispatched()
			accounted := false
			if err != nil && accumulator != nil {
				if detail, ok := retryWithoutPenaltyUsageDetail(err); ok {
					accumulator.Add(detail)
					accounted = true
				}
			}
			resultCh <- retryWithoutPenaltyHedgeLaneResult{
				name:           name,
				authID:         tracker.selectedAuthID(),
				response:       resp,
				err:            err,
				dispatched:     dispatched,
				usageAccounted: accounted,
			}
		}()
		return tracker
	}

	cancelAll := func(handles map[string]retryWithoutPenaltyHedgeLaneHandle) {
		for _, handle := range handles {
			if handle.cancel != nil {
				handle.cancel()
			}
		}
	}

	for attempts < remainingRetries {
		resultCh := make(chan retryWithoutPenaltyHedgeLaneResult, 2)
		handles := make(map[string]retryWithoutPenaltyHedgeLaneHandle, 2)
		pending := 0
		waveRemaining := remainingRetries - attempts
		var results []retryWithoutPenaltyHedgeLaneResult

		primaryTracker := startLane(resultCh, handles, &pending, "primary", nil, nil)
		if primaryTracker == nil {
			break
		}
		secondAllowed := !disableSecondLane && retryWithoutPenaltySecondLaneAllowed(policy, waveRemaining, state)
		var timer *time.Timer
		var timerC <-chan time.Time
		if secondAllowed {
			timer = time.NewTimer(policy.hedgeDelay)
			timerC = timer.C
		}

		for pending > 0 {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				cancelAll(handles)
				return retryWithoutPenaltyHedgeOutcome{err: ctx.Err(), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			case res := <-resultCh:
				pending--
				if res.dispatched {
					attempts++
				} else if res.name == "secondary" {
					disableSecondLane = true
				}
				if !res.dispatched && reservedAttempts > 0 {
					reservedAttempts--
				}
				if res.usageAccounted {
					usageAccounted = true
				}
				results = append(results, res)
			case <-timerC:
				timerC = nil
				if pending <= 0 || reservedAttempts >= remainingRetries {
					continue
				}
				exclude := retryWithoutPenaltySecondLaneExcludes(policy, primaryTracker.selectedAuthID())
				primaryCancel := handles["primary"].cancel
				startLane(resultCh, handles, &pending, "secondary", exclude, func(string) {
					if policy.requireDistinctAuth && primaryTracker.selectedAuthID() == "" && primaryCancel != nil {
						primaryCancel()
					}
				})
			}
		}
		if timer != nil {
			timer.Stop()
		}

		retryWithoutPenaltyConsiderQualityFallbackCandidates(results, fallbackCandidate)

		if winner := retryWithoutPenaltySelectQualityResponseWinner(results); winner >= 0 {
			if retryWithoutPenaltyAddQualityResponseLosers(results, winner, accumulator) {
				usageAccounted = true
			}
			resp := retryWithoutPenaltyFinalizeQualityResponse(results[winner].response, accumulator)
			if fallbackResp, ok := retryWithoutPenaltyMaybeSelectFallbackResponse(resp, fallbackCandidate, accumulator); ok {
				retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				return retryWithoutPenaltyHedgeOutcome{response: fallbackResp, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			}
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, results[winner].authID)
			return retryWithoutPenaltyHedgeOutcome{response: resp, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}

		waveHadAbnormal, waveOrdinaryErr := retryWithoutPenaltySummarizeQualityErrors(results, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr, fallbackCandidate)
		if waveHadAbnormal {
			if attempts >= remainingRetries {
				out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				}
				return out
			}
			continue
		}
		if waveOrdinaryErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: waveOrdinaryErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
		if lastZeroDispatchErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
	}

	if lastAbnormalErr != nil {
		out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
		}
		return out
	}
	return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func (m *Manager) executeStreamRetryWithoutPenaltyHedgedQuality(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) retryWithoutPenaltyHedgeOutcome {
	attempts := 0
	reservedAttempts := 0
	usageAccounted := false
	disableSecondLane := false
	var lastAbnormalErr error
	var lastAbnormalAuthID string
	var lastZeroDispatchErr error
	selectedCallback := retryWithoutPenaltyHedgeSelectedAuthCallback(opts)
	var coordinator *retryWithoutPenaltyHedgeLaneCoordinator
	if policy.requireDistinctAuth {
		coordinator = newRetryWithoutPenaltyHedgeLaneCoordinator()
	}

	startLane := func(resultCh chan<- retryWithoutPenaltyHedgeLaneResult, handles map[string]retryWithoutPenaltyHedgeLaneHandle, pending *int, name string, excludeAuthIDs []string, onAcceptedAuth func(string)) *retryWithoutPenaltyHedgeLaneTracker {
		if reservedAttempts >= remainingRetries {
			return nil
		}
		laneCtx, cancel := context.WithCancel(ctx)
		tracker := newRetryWithoutPenaltyHedgeLaneTracker()
		laneSelectedCallback := tracker.selectedCallback()
		if coordinator != nil {
			laneSelectedCallback = func(authID string) {
				if !coordinator.claim(name, authID) {
					cancel()
					return
				}
				tracker.markSelected(authID)
				if onAcceptedAuth != nil {
					onAcceptedAuth(authID)
				}
			}
		}
		laneOpts := retryWithoutPenaltyHedgeOptions(opts, accumulator, excludeAuthIDs, laneSelectedCallback)
		laneReq := cloneRetryWithoutPenaltyHedgeRequest(req)
		handles[name] = retryWithoutPenaltyHedgeLaneHandle{name: name, cancel: cancel}
		(*pending)++
		reservedAttempts++
		go func() {
			defer coordinator.release(name)
			stream, err := m.executeStreamMixedOnce(laneCtx, providers, laneReq, laneOpts, maxRetryCredentials)
			var headers http.Header
			var chunks []cliproxyexecutor.StreamChunk
			var metadata map[string]any
			if err == nil && stream != nil {
				headers = cloneHTTPHeader(stream.Headers)
				metadata = cloneSchedulerAnyMap(stream.Metadata)
				chunks, err = collectRetryWithoutPenaltyStreamChunks(laneCtx, stream.Chunks, policy.streamBufferMaxBytes)
			}
			dispatched := tracker.dispatched()
			accounted := false
			if err != nil && accumulator != nil {
				if detail, ok := retryWithoutPenaltyUsageDetail(err); ok {
					accumulator.Add(detail)
					accounted = true
				}
			}
			resultCh <- retryWithoutPenaltyHedgeLaneResult{
				name:           name,
				authID:         tracker.selectedAuthID(),
				streamHeaders:  headers,
				streamChunks:   chunks,
				streamMetadata: metadata,
				err:            err,
				dispatched:     dispatched,
				usageAccounted: accounted,
			}
		}()
		return tracker
	}

	cancelAll := func(handles map[string]retryWithoutPenaltyHedgeLaneHandle) {
		for _, handle := range handles {
			if handle.cancel != nil {
				handle.cancel()
			}
		}
	}

	for attempts < remainingRetries {
		resultCh := make(chan retryWithoutPenaltyHedgeLaneResult, 2)
		handles := make(map[string]retryWithoutPenaltyHedgeLaneHandle, 2)
		pending := 0
		waveRemaining := remainingRetries - attempts
		var results []retryWithoutPenaltyHedgeLaneResult

		primaryTracker := startLane(resultCh, handles, &pending, "primary", nil, nil)
		if primaryTracker == nil {
			break
		}
		secondAllowed := !disableSecondLane && retryWithoutPenaltySecondLaneAllowed(policy, waveRemaining, state)
		var timer *time.Timer
		var timerC <-chan time.Time
		if secondAllowed {
			timer = time.NewTimer(policy.hedgeDelay)
			timerC = timer.C
		}

		for pending > 0 {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				cancelAll(handles)
				return retryWithoutPenaltyHedgeOutcome{err: ctx.Err(), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			case res := <-resultCh:
				pending--
				if res.dispatched {
					attempts++
				} else if res.name == "secondary" {
					disableSecondLane = true
				}
				if !res.dispatched && reservedAttempts > 0 {
					reservedAttempts--
				}
				if res.usageAccounted {
					usageAccounted = true
				}
				results = append(results, res)
			case <-timerC:
				timerC = nil
				if pending <= 0 || reservedAttempts >= remainingRetries {
					continue
				}
				exclude := retryWithoutPenaltySecondLaneExcludes(policy, primaryTracker.selectedAuthID())
				primaryCancel := handles["primary"].cancel
				startLane(resultCh, handles, &pending, "secondary", exclude, func(string) {
					if policy.requireDistinctAuth && primaryTracker.selectedAuthID() == "" && primaryCancel != nil {
						primaryCancel()
					}
				})
			}
		}
		if timer != nil {
			timer.Stop()
		}

		retryWithoutPenaltyConsiderQualityFallbackCandidates(results, fallbackCandidate)

		if winner := retryWithoutPenaltySelectQualityStreamWinner(results); winner >= 0 {
			if retryWithoutPenaltyAddQualityStreamLosers(results, winner, accumulator) {
				usageAccounted = true
			}
			stream := retryWithoutPenaltyFinalizeQualityStream(results[winner], accumulator)
			if fallbackStream, ok := retryWithoutPenaltyMaybeSelectFallbackStreamResult(stream, fallbackCandidate, accumulator); ok {
				retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				return retryWithoutPenaltyHedgeOutcome{stream: fallbackStream, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
			}
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, results[winner].authID)
			return retryWithoutPenaltyHedgeOutcome{stream: stream, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}

		waveHadAbnormal, waveOrdinaryErr := retryWithoutPenaltySummarizeQualityErrors(results, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr, fallbackCandidate)
		if waveHadAbnormal {
			if attempts >= remainingRetries {
				out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
				}
				return out
			}
			continue
		}
		if waveOrdinaryErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: waveOrdinaryErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
		if lastZeroDispatchErr != nil {
			return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
		}
	}

	if lastAbnormalErr != nil {
		out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, fallbackCandidate.Err(lastAbnormalErr), attempts, disableSecondLane, usageAccounted, fallbackCandidate, accumulator)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, fallbackCandidate.AuthID(lastAbnormalAuthID))
		}
		return out
	}
	return retryWithoutPenaltyHedgeOutcome{err: lastZeroDispatchErr, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func processRetryWithoutPenaltyExecuteHedgeResult(res retryWithoutPenaltyHedgeLaneResult, attempts *int, reservedAttempts *int, usageAccounted *bool, disableSecondLane *bool, waveHadAbnormal *bool, waveOrdinaryErr *error, lastAbnormalErr *error, lastAbnormalAuthID *string, lastZeroDispatchErr *error, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) (retryWithoutPenaltyHedgeOutcome, bool) {
	if res.dispatched {
		(*attempts)++
	} else if res.name == "secondary" {
		*disableSecondLane = true
	}
	if !res.dispatched && reservedAttempts != nil && *reservedAttempts > 0 {
		(*reservedAttempts)--
	}
	if res.usageAccounted {
		*usageAccounted = true
	}
	if res.err == nil {
		return retryWithoutPenaltyHedgeOutcome{response: res.response}, true
	}
	if res.dispatched {
		if isRetryWithoutPenaltyError(res.err) {
			*waveHadAbnormal = true
			fallbackCandidate.Consider(res.err, res.authID)
			*lastAbnormalErr = res.err
			if lastAbnormalAuthID != nil {
				*lastAbnormalAuthID = res.authID
			}
		} else {
			*waveOrdinaryErr = res.err
		}
	} else {
		*lastZeroDispatchErr = res.err
	}
	return retryWithoutPenaltyHedgeOutcome{}, false
}

func processRetryWithoutPenaltyStreamHedgeResult(res retryWithoutPenaltyHedgeLaneResult, winnerCancel context.CancelFunc, attempts *int, reservedAttempts *int, usageAccounted *bool, disableSecondLane *bool, waveHadAbnormal *bool, waveOrdinaryErr *error, lastAbnormalErr *error, lastAbnormalAuthID *string, lastZeroDispatchErr *error, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) (retryWithoutPenaltyHedgeOutcome, bool) {
	if res.dispatched {
		(*attempts)++
	} else if res.name == "secondary" {
		*disableSecondLane = true
	}
	if !res.dispatched && reservedAttempts != nil && *reservedAttempts > 0 {
		(*reservedAttempts)--
	}
	if res.usageAccounted {
		*usageAccounted = true
	}
	if res.err == nil {
		return retryWithoutPenaltyHedgeOutcome{stream: retryWithoutPenaltyStreamResultWithCancel(res.stream, winnerCancel)}, true
	}
	if res.dispatched {
		if isRetryWithoutPenaltyError(res.err) {
			*waveHadAbnormal = true
			fallbackCandidate.Consider(res.err, res.authID)
			*lastAbnormalErr = res.err
			if lastAbnormalAuthID != nil {
				*lastAbnormalAuthID = res.authID
			}
		} else {
			*waveOrdinaryErr = res.err
		}
	} else {
		*lastZeroDispatchErr = res.err
	}
	return retryWithoutPenaltyHedgeOutcome{}, false
}

func retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class string, err error, attempts int, disableSecondLane bool, usageAccounted bool, fallbackCandidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) retryWithoutPenaltyHedgeOutcome {
	if resp, ok := retryWithoutPenaltyCandidateFallbackResponse(err, fallbackCandidate, accumulator); ok {
		return retryWithoutPenaltyHedgeOutcome{response: resp, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
	}
	return retryWithoutPenaltyHedgeOutcome{err: newRetryWithoutPenaltyExhaustedError(err, class), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func retryWithoutPenaltyStreamHedgeExhaustedOutcome(class string, err error, attempts int, disableSecondLane bool, usageAccounted bool, fallbackCandidate *retryWithoutPenaltyFallbackCandidate, accumulator *cliproxyexecutor.UsageAccumulator) retryWithoutPenaltyHedgeOutcome {
	if stream, ok := retryWithoutPenaltyCandidateFallbackStreamResult(err, fallbackCandidate, accumulator); ok {
		return retryWithoutPenaltyHedgeOutcome{stream: stream, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
	}
	return retryWithoutPenaltyHedgeOutcome{err: newRetryWithoutPenaltyExhaustedError(err, class), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func drainRetryWithoutPenaltyStreamHedgeResults(resultCh <-chan retryWithoutPenaltyHedgeLaneResult, pending int) {
	if pending <= 0 {
		return
	}
	go func() {
		for i := 0; i < pending; i++ {
			res := <-resultCh
			if res.stream != nil {
				discardStreamChunks(res.stream.Chunks)
			}
		}
	}()
}

func collectRetryWithoutPenaltyStreamChunks(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk, maxBytes int64) ([]cliproxyexecutor.StreamChunk, error) {
	var chunks []cliproxyexecutor.StreamChunk
	var bufferedBytes int64
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				discardStreamChunks(ch)
				return nil, ctx.Err()
			case chunk, ok = <-ch:
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return chunks, nil
		}
		if chunk.Err != nil {
			discardStreamChunks(ch)
			return nil, chunk.Err
		}
		if maxBytes > 0 && bufferedBytes+int64(len(chunk.Payload)) > maxBytes {
			discardStreamChunks(ch)
			return nil, retryWithoutPenaltyStreamBufferLimitError{limit: maxBytes}
		}
		if len(chunk.Payload) > 0 {
			chunk.Payload = bytes.Clone(chunk.Payload)
		}
		chunks = append(chunks, chunk)
		bufferedBytes += int64(len(chunk.Payload))
	}
}

func retryWithoutPenaltySelectQualityResponseWinner(results []retryWithoutPenaltyHedgeLaneResult) int {
	winner := -1
	var winnerScore int64
	var winnerPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
	for i, res := range results {
		if !res.dispatched || res.err != nil {
			continue
		}
		detail, _ := retryWithoutPenaltyResponseUsage(res.response.Metadata)
		policy, hasPolicy := retryWithoutPenaltyCandidatePolicyFromMetadata(res.response.Metadata, detail)
		if hasPolicy && policy.DeliveryPolicy == retryWithoutPenaltyDeliveryPolicyLatest {
			winner = i
			winnerScore = retryWithoutPenaltyHedgeScore(res.response.Metadata)
			winnerPolicy = policy
			continue
		}
		score := retryWithoutPenaltyHedgeScore(res.response.Metadata)
		if winner < 0 || retryWithoutPenaltyResponseCandidateBetter(winnerScore, winnerPolicy, score, policy, hasPolicy) {
			winner = i
			winnerScore = score
			winnerPolicy = policy
		}
	}
	return winner
}

func retryWithoutPenaltySelectQualityStreamWinner(results []retryWithoutPenaltyHedgeLaneResult) int {
	winner := -1
	var winnerScore int64
	var winnerPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
	for i, res := range results {
		if !res.dispatched || res.err != nil {
			continue
		}
		detail, score, ok := retryWithoutPenaltyStreamUsage(res.streamMetadata)
		if !ok {
			score = retryWithoutPenaltyHedgeScore(res.streamMetadata)
		}
		policy, hasPolicy := retryWithoutPenaltyCandidatePolicyFromMetadata(res.streamMetadata, detail)
		if hasPolicy && policy.DeliveryPolicy == retryWithoutPenaltyDeliveryPolicyLatest {
			winner = i
			winnerScore = score
			winnerPolicy = policy
			continue
		}
		if winner < 0 || retryWithoutPenaltyResponseCandidateBetter(winnerScore, winnerPolicy, score, policy, hasPolicy) {
			winner = i
			winnerScore = score
			winnerPolicy = policy
		}
	}
	return winner
}

func retryWithoutPenaltyAddQualityResponseLosers(results []retryWithoutPenaltyHedgeLaneResult, winner int, accumulator *cliproxyexecutor.UsageAccumulator) bool {
	if accumulator == nil {
		return false
	}
	accounted := false
	for i, res := range results {
		if i == winner || !res.dispatched || res.err != nil {
			continue
		}
		if detail, ok := retryWithoutPenaltyResponseUsage(res.response.Metadata); ok {
			accumulator.Add(detail)
			accounted = true
		}
	}
	return accounted
}

func retryWithoutPenaltyAddQualityStreamLosers(results []retryWithoutPenaltyHedgeLaneResult, winner int, accumulator *cliproxyexecutor.UsageAccumulator) bool {
	if accumulator == nil {
		return false
	}
	accounted := false
	for i, res := range results {
		if i == winner || !res.dispatched || res.err != nil {
			continue
		}
		if detail, _, ok := retryWithoutPenaltyStreamUsage(res.streamMetadata); ok {
			accumulator.Add(detail)
			accounted = true
		}
	}
	return accounted
}

func retryWithoutPenaltyConsiderQualityFallbackCandidates(results []retryWithoutPenaltyHedgeLaneResult, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) {
	if fallbackCandidate == nil {
		return
	}
	for _, res := range results {
		if res.err == nil || !res.dispatched || !isRetryWithoutPenaltyError(res.err) {
			continue
		}
		fallbackCandidate.Consider(res.err, res.authID)
	}
}

func retryWithoutPenaltySummarizeQualityErrors(results []retryWithoutPenaltyHedgeLaneResult, lastAbnormalErr *error, lastAbnormalAuthID *string, lastZeroDispatchErr *error, fallbackCandidate *retryWithoutPenaltyFallbackCandidate) (bool, error) {
	waveHadAbnormal := false
	var waveOrdinaryErr error
	for _, res := range results {
		if res.err == nil {
			continue
		}
		if res.dispatched {
			if isRetryWithoutPenaltyError(res.err) {
				waveHadAbnormal = true
				fallbackCandidate.Consider(res.err, res.authID)
				if lastAbnormalErr != nil {
					*lastAbnormalErr = res.err
				}
				if lastAbnormalAuthID != nil {
					*lastAbnormalAuthID = res.authID
				}
			} else if waveOrdinaryErr == nil {
				waveOrdinaryErr = res.err
			}
			continue
		}
		if lastZeroDispatchErr != nil {
			*lastZeroDispatchErr = res.err
		}
	}
	return waveHadAbnormal, waveOrdinaryErr
}

func retryWithoutPenaltyFinalizeQualityResponse(resp cliproxyexecutor.Response, accumulator *cliproxyexecutor.UsageAccumulator) cliproxyexecutor.Response {
	finalizer, _ := resp.Metadata[cliproxyexecutor.RetryWithoutPenaltyResponseFinalizerMetadataKey].(cliproxyexecutor.RetryWithoutPenaltyResponseFinalizer)
	if finalizer == nil {
		return resp
	}
	return finalizer(resp, retryWithoutPenaltyAccumulatorSnapshot(accumulator))
}

func retryWithoutPenaltyFinalizeQualityStream(res retryWithoutPenaltyHedgeLaneResult, accumulator *cliproxyexecutor.UsageAccumulator) *cliproxyexecutor.StreamResult {
	finalizer, _ := res.streamMetadata[cliproxyexecutor.RetryWithoutPenaltyStreamFinalizerMetadataKey].(cliproxyexecutor.RetryWithoutPenaltyStreamFinalizer)
	if finalizer != nil {
		return finalizer(res.streamHeaders, res.streamChunks, retryWithoutPenaltyAccumulatorSnapshot(accumulator))
	}
	ch := make(chan cliproxyexecutor.StreamChunk, len(res.streamChunks))
	for i := range res.streamChunks {
		chunk := res.streamChunks[i]
		if len(chunk.Payload) > 0 {
			chunk.Payload = bytes.Clone(chunk.Payload)
		}
		ch <- chunk
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers:  cloneHTTPHeader(res.streamHeaders),
		Chunks:   ch,
		Metadata: cloneSchedulerAnyMap(res.streamMetadata),
	}
}

func retryWithoutPenaltyAccumulatorSnapshot(accumulator *cliproxyexecutor.UsageAccumulator) cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot {
	if accumulator == nil {
		return cliproxyexecutor.RetryWithoutPenaltyUsageSnapshot{}
	}
	return accumulator.RetryWithoutPenaltySnapshot()
}

func retryWithoutPenaltyResponseUsage(meta map[string]any) (coreusage.Detail, bool) {
	return retryWithoutPenaltyUsageFromMetadata(meta)
}

func retryWithoutPenaltyStreamUsage(meta map[string]any) (coreusage.Detail, int64, bool) {
	if len(meta) == 0 {
		return coreusage.Detail{}, 0, false
	}
	switch value := meta[cliproxyexecutor.RetryWithoutPenaltyStreamUsageMetadataKey].(type) {
	case *cliproxyexecutor.RetryWithoutPenaltyStreamUsage:
		if value == nil || !value.OK || !hasRetryWithoutPenaltyUsageDetail(value.Detail) {
			return coreusage.Detail{}, 0, false
		}
		return value.Detail, value.HedgeScore, true
	case cliproxyexecutor.RetryWithoutPenaltyStreamUsage:
		if !value.OK || !hasRetryWithoutPenaltyUsageDetail(value.Detail) {
			return coreusage.Detail{}, 0, false
		}
		return value.Detail, value.HedgeScore, true
	default:
		detail, ok := retryWithoutPenaltyUsageFromMetadata(meta)
		return detail, retryWithoutPenaltyHedgeScore(meta), ok
	}
}

func retryWithoutPenaltyUsageFromMetadata(meta map[string]any) (coreusage.Detail, bool) {
	if len(meta) == 0 {
		return coreusage.Detail{}, false
	}
	switch detail := meta[cliproxyexecutor.RetryWithoutPenaltyUsageDetailMetadataKey].(type) {
	case coreusage.Detail:
		return detail, hasRetryWithoutPenaltyUsageDetail(detail)
	case *coreusage.Detail:
		if detail == nil {
			return coreusage.Detail{}, false
		}
		return *detail, hasRetryWithoutPenaltyUsageDetail(*detail)
	default:
		return coreusage.Detail{}, false
	}
}

func retryWithoutPenaltyCandidatePolicyFromMetadata(meta map[string]any, detail coreusage.Detail) (cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, bool) {
	if len(meta) == 0 {
		return cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy{}, false
	}
	var policy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy
	switch value := meta[cliproxyexecutor.RetryWithoutPenaltyCandidatePolicyMetadataKey].(type) {
	case cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy:
		policy = value
	case *cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy:
		if value == nil {
			return cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy{}, false
		}
		policy = *value
	default:
		switch value := meta[cliproxyexecutor.RetryWithoutPenaltyStreamUsageMetadataKey].(type) {
		case *cliproxyexecutor.RetryWithoutPenaltyStreamUsage:
			if value == nil {
				return cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy{}, false
			}
			policy = value.CandidatePolicy
		case cliproxyexecutor.RetryWithoutPenaltyStreamUsage:
			policy = value.CandidatePolicy
		default:
			return cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy{}, false
		}
	}
	policy = normalizeRetryWithoutPenaltyCandidatePolicy(policy, detail)
	if policy.DeliveryPolicy == "" && policy.FallbackPolicy == "" && policy.CandidateKind == "" {
		return cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy{}, false
	}
	return policy, true
}

func retryWithoutPenaltyResponseCandidateBetter(currentScore int64, currentPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, nextScore int64, nextPolicy cliproxyexecutor.RetryWithoutPenaltyCandidatePolicy, hasNextPolicy bool) bool {
	if !hasNextPolicy {
		return nextScore > currentScore
	}
	switch nextPolicy.DeliveryPolicy {
	case retryWithoutPenaltyDeliveryPolicyLatest:
		return true
	case retryWithoutPenaltyDeliveryPolicyFirstNonSpecial:
		return false
	case retryWithoutPenaltyDeliveryPolicyBestNonSpecial:
		if currentPolicy.CandidateKind != nextPolicy.CandidateKind {
			return nextPolicy.CandidateKind == cliproxyexecutor.RetryWithoutPenaltyCandidateKindNonSpecial
		}
		fallthrough
	case retryWithoutPenaltyDeliveryPolicyMaxOutput:
		if nextScore != currentScore {
			return nextScore > currentScore
		}
		if nextPolicy.VisibleTokens != currentPolicy.VisibleTokens {
			return nextPolicy.VisibleTokens > currentPolicy.VisibleTokens
		}
		return false
	default:
		if nextScore != currentScore {
			return nextScore > currentScore
		}
		if nextPolicy.VisibleTokens != currentPolicy.VisibleTokens {
			return nextPolicy.VisibleTokens > currentPolicy.VisibleTokens
		}
		return false
	}
}

func retryWithoutPenaltyHedgeScore(meta map[string]any) int64 {
	if len(meta) == 0 {
		return 0
	}
	switch score := meta[cliproxyexecutor.RetryWithoutPenaltyHedgeScoreMetadataKey].(type) {
	case int:
		return int64(score)
	case int64:
		return score
	case int32:
		return int64(score)
	case float64:
		return int64(score)
	case float32:
		return int64(score)
	default:
		if detail, ok := retryWithoutPenaltyUsageFromMetadata(meta); ok {
			return detail.OutputTokens
		}
		return 0
	}
}

func retryWithoutPenaltySecondLaneAllowed(policy retryWithoutPenaltyHedgePolicy, remainingRetries int, state *retryWithoutPenaltyHedgeRequestState) bool {
	if remainingRetries < 2 {
		return false
	}
	if state != nil && state.secondLaneDisabled {
		return false
	}
	if policy.requireDistinctAuth && strings.TrimSpace(policy.triggerAuthID) == "" {
		return false
	}
	return true
}

func retryWithoutPenaltySecondLaneExcludes(policy retryWithoutPenaltyHedgePolicy, primaryAuthID string) []string {
	if !policy.requireDistinctAuth {
		return nil
	}
	var excludes []string
	excludes = appendUniqueRetryWithoutPenaltyAuthID(excludes, policy.triggerAuthID)
	excludes = appendUniqueRetryWithoutPenaltyAuthID(excludes, primaryAuthID)
	return excludes
}

func appendUniqueRetryWithoutPenaltyAuthID(ids []string, authID string) []string {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == authID {
			return ids
		}
	}
	return append(ids, authID)
}

func retryWithoutPenaltyHedgeOptions(opts cliproxyexecutor.Options, accumulator *cliproxyexecutor.UsageAccumulator, excludeAuthIDs []string, selectedCallback func(string)) cliproxyexecutor.Options {
	out := opts
	out.Headers = cloneRequestHeaders(opts.Headers)
	out.Query = cloneURLValues(opts.Query)
	out.OriginalRequest = bytes.Clone(opts.OriginalRequest)

	meta := make(map[string]any, len(opts.Metadata)+3)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	if accumulator != nil && hasRetryWithoutPenaltyUsageDetail(accumulator.Snapshot()) {
		meta[cliproxyexecutor.CodexAbnormalReasoningRetryUsageMetadataKey] = accumulator
	}
	if len(excludeAuthIDs) > 0 {
		merged := retryWithoutPenaltyHedgeExcludeAuthIDs(opts.Metadata, excludeAuthIDs)
		if len(merged) > 0 {
			meta[cliproxyexecutor.ExcludeAuthIDsMetadataKey] = merged
		}
	}
	if selectedCallback != nil {
		meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = selectedCallback
	}
	out.Metadata = meta
	return out
}

func retryWithoutPenaltyHedgeSelectedAuthCallback(opts cliproxyexecutor.Options) func(string) {
	if callback, ok := opts.Metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok {
		return callback
	}
	return nil
}

func retryWithoutPenaltyHedgePublishSelectedAuthCallback(callback func(string), authID string) {
	if callback == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	callback(authID)
}

func retryWithoutPenaltyHedgeExcludeAuthIDs(meta map[string]any, extra []string) []string {
	excluded := excludedAuthIDsFromMetadata(meta)
	for _, authID := range extra {
		authID = strings.TrimSpace(authID)
		if authID != "" {
			excluded[authID] = struct{}{}
		}
	}
	if len(excluded) == 0 {
		return nil
	}
	out := make([]string, 0, len(excluded))
	for authID := range excluded {
		out = append(out, authID)
	}
	sort.Strings(out)
	return out
}

func cloneURLValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	out := make(url.Values, len(values))
	for key, items := range values {
		out[key] = append([]string(nil), items...)
	}
	return out
}

func cloneRetryWithoutPenaltyHedgeRequest(req cliproxyexecutor.Request) cliproxyexecutor.Request {
	out := req
	out.Payload = bytes.Clone(req.Payload)
	if req.Metadata != nil {
		meta := make(map[string]any, len(req.Metadata))
		for key, value := range req.Metadata {
			meta[key] = value
		}
		out.Metadata = meta
	}
	return out
}

func retryWithoutPenaltyStreamResultWithCancel(result *cliproxyexecutor.StreamResult, cancel context.CancelFunc) *cliproxyexecutor.StreamResult {
	if result == nil || cancel == nil {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer cancel()
		defer close(out)
		for chunk := range result.Chunks {
			out <- chunk
		}
	}()
	return &cliproxyexecutor.StreamResult{
		Headers:  cloneHTTPHeader(result.Headers),
		Chunks:   out,
		Metadata: cloneSchedulerAnyMap(result.Metadata),
	}
}
