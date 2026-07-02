package auth

import (
	"bytes"
	"context"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	err            error
	dispatched     bool
	usageAccounted bool
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

func (m *Manager) executeRetryWithoutPenaltyHedged(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState) retryWithoutPenaltyHedgeOutcome {
	if remainingRetries <= 0 {
		return retryWithoutPenaltyHedgeOutcome{}
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
				if out, done := processRetryWithoutPenaltyExecuteHedgeResult(res, &attempts, &reservedAttempts, &usageAccounted, &disableSecondLane, &waveHadAbnormal, &waveOrdinaryErr, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr); done {
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
				out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, lastAbnormalErr, attempts, disableSecondLane, usageAccounted)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, lastAbnormalAuthID)
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
		out := retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class, lastAbnormalErr, attempts, disableSecondLane, usageAccounted)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, lastAbnormalAuthID)
		}
		return out
	}
	err := lastZeroDispatchErr
	return retryWithoutPenaltyHedgeOutcome{err: err, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func (m *Manager) executeStreamRetryWithoutPenaltyHedged(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int, class string, policy retryWithoutPenaltyHedgePolicy, remainingRetries int, accumulator *cliproxyexecutor.UsageAccumulator, state *retryWithoutPenaltyHedgeRequestState) retryWithoutPenaltyHedgeOutcome {
	if remainingRetries <= 0 {
		return retryWithoutPenaltyHedgeOutcome{}
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
				if out, done := processRetryWithoutPenaltyStreamHedgeResult(res, handles[res.name].cancel, &attempts, &reservedAttempts, &usageAccounted, &disableSecondLane, &waveHadAbnormal, &waveOrdinaryErr, &lastAbnormalErr, &lastAbnormalAuthID, &lastZeroDispatchErr); done {
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
				out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, lastAbnormalErr, attempts, disableSecondLane, usageAccounted)
				if out.err == nil {
					retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, lastAbnormalAuthID)
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
		out := retryWithoutPenaltyStreamHedgeExhaustedOutcome(class, lastAbnormalErr, attempts, disableSecondLane, usageAccounted)
		if out.err == nil {
			retryWithoutPenaltyHedgePublishSelectedAuthCallback(selectedCallback, lastAbnormalAuthID)
		}
		return out
	}
	err := lastZeroDispatchErr
	return retryWithoutPenaltyHedgeOutcome{err: err, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func processRetryWithoutPenaltyExecuteHedgeResult(res retryWithoutPenaltyHedgeLaneResult, attempts *int, reservedAttempts *int, usageAccounted *bool, disableSecondLane *bool, waveHadAbnormal *bool, waveOrdinaryErr *error, lastAbnormalErr *error, lastAbnormalAuthID *string, lastZeroDispatchErr *error) (retryWithoutPenaltyHedgeOutcome, bool) {
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

func processRetryWithoutPenaltyStreamHedgeResult(res retryWithoutPenaltyHedgeLaneResult, winnerCancel context.CancelFunc, attempts *int, reservedAttempts *int, usageAccounted *bool, disableSecondLane *bool, waveHadAbnormal *bool, waveOrdinaryErr *error, lastAbnormalErr *error, lastAbnormalAuthID *string, lastZeroDispatchErr *error) (retryWithoutPenaltyHedgeOutcome, bool) {
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

func retryWithoutPenaltyExecuteHedgeExhaustedOutcome(class string, err error, attempts int, disableSecondLane bool, usageAccounted bool) retryWithoutPenaltyHedgeOutcome {
	if resp, ok := retryWithoutPenaltyFallbackResponse(err); ok {
		return retryWithoutPenaltyHedgeOutcome{response: resp, attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
	}
	return retryWithoutPenaltyHedgeOutcome{err: newRetryWithoutPenaltyExhaustedError(err, class), attempts: attempts, disableSecondLane: disableSecondLane, usageAccounted: usageAccounted}
}

func retryWithoutPenaltyStreamHedgeExhaustedOutcome(class string, err error, attempts int, disableSecondLane bool, usageAccounted bool) retryWithoutPenaltyHedgeOutcome {
	if stream, ok := retryWithoutPenaltyFallbackStreamResult(err); ok {
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
		Headers: cloneHTTPHeader(result.Headers),
		Chunks:  out,
	}
}
