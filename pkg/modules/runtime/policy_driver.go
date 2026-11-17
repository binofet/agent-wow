package modrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
)

var policyPhase = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type policyFeed struct {
	phase, method string
}

// StartObservedPolicy connects reflected read feeds to a single Lua writer.
// The final feed supplies the decision phase. Earlier feeds update context.
func (m *Manager) StartObservedPolicy(ctx context.Context, source, binding string, contextBindings ...string) error {
	catalog, _, err := m.observedSession()
	if err != nil {
		return err
	}
	var feeds []policyFeed
	phases := make(map[string]bool)
	for _, entry := range append(append([]string(nil), contextBindings...), binding) {
		phase, method, ok := strings.Cut(entry, "=")
		if !ok || !policyPhase.MatchString(phase) || phases[phase] {
			return fmt.Errorf("policy observation must use a unique phase=module.alias binding")
		}
		phases[phase] = true
		feed, err := catalog.Catalog.ForMethod(method)
		if err != nil {
			return err
		}
		if feed.Methods[0].Effect != moddisc.EffectRead {
			return fmt.Errorf("policy observation %q must be a reflected read", method)
		}
		if _, err := m.readPolicyObservation(ctx, catalog.SessionID, method); err != nil {
			return err
		}
		feeds = append(feeds, policyFeed{phase: phase, method: method})
	}
	host, err := m.StartPolicy(ctx, source)
	if err != nil {
		return err
	}
	go host.runObservation(ctx, catalog.SessionID, feeds)
	return nil
}

func (p *SessionPolicy) runObservation(parent context.Context, sid string, feeds []policyFeed) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()

	// Observe at most once per interval. The module's data stays live between
	// decisions, and delayed RPCs never overlap ticks or advance twice.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastProblem string
	phase := feeds[len(feeds)-1].phase
	for ctx.Err() == nil {
		err := p.stepObservation(ctx, sid, feeds)
		if err != nil && ctx.Err() == nil {
			if message := err.Error(); message != lastProblem {
				p.manager.logger.Warn("Lua observation step failed", "phase", phase, "error", message)
				lastProblem = message
			}
		} else if lastProblem != "" {
			p.manager.logger.Info("Lua observation resumed", "phase", phase)
			lastProblem = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *SessionPolicy) stepObservation(ctx context.Context, sid string, feeds []policyFeed) error {
	observations := make([]map[string]any, len(feeds))
	for i, feed := range feeds {
		observed, err := p.manager.readPolicyObservation(ctx, sid, feed.method)
		if err != nil {
			return err
		}
		observations[i] = observed
	}
	sequences := make(map[string]any, len(feeds))
	for i, feed := range feeds {
		sequence, err := p.enqueueObservation(ctx, feed.phase, observations[i])
		if err != nil {
			return err
		}
		sequences[feed.phase] = strconv.FormatUint(sequence, 10)
	}
	phase := feeds[len(feeds)-1].phase
	_, _, err := p.Step(ctx, map[string]any{
		"kind": phase, "observationSequence": sequences[phase], "observationSequences": sequences,
	}, nil)
	return err
}

func (p *SessionPolicy) enqueueObservation(ctx context.Context, phase string, observed map[string]any) (uint64, error) {
	event := behavior.Event{Kind: phase + "_observation", Value: observed}
	sequence, err := p.Enqueue(event)
	if errors.Is(err, behavior.ErrEventBackpressure) {
		// Apply buffered events before retrying the unwritten observation.
		// This does not re-run an action or skip an earlier receipt.
		p.advance.Lock()
		drainErr := p.vm.Drain(ctx)
		p.advance.Unlock()
		if drainErr != nil {
			return 0, drainErr
		}
		sequence, err = p.Enqueue(event)
	}
	return sequence, err
}

func (m *Manager) readPolicyObservation(ctx context.Context, sid, method string) (map[string]any, error) {
	body, err := m.invokeJSON(ctx, method, nil, true)
	if err != nil {
		return nil, err
	}
	var observed map[string]any
	if err := json.Unmarshal(body, &observed); err != nil {
		return nil, err
	}
	if observed["sessionId"] != sid {
		return nil, fmt.Errorf("policy observation %q did not identify the current session", method)
	}
	if owned, present := observed["hostOwned"]; present && owned != true {
		return nil, fmt.Errorf("policy observation %q reports the native writer still owns the action", method)
	}
	return observed, nil
}
