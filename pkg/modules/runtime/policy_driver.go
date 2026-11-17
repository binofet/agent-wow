package modrt

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/pkg/behavior"
	moddisc "github.com/agent-wow/agent-wow/pkg/modules/discovery"
)

var policyPhase = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// StartObservedPolicy connects a reflected observation to a Lua policy. It
// forwards the typed observation without assigning any gameplay priorities.
func (m *Manager) StartObservedPolicy(ctx context.Context, source, binding string) error {
	phase, method, ok := strings.Cut(binding, "=")
	if !ok || !policyPhase.MatchString(phase) {
		return fmt.Errorf("policy observation must be phase=module.alias")
	}
	catalog, _, err := m.observedSession()
	if err != nil {
		return err
	}
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
	host, err := m.StartPolicy(ctx, source)
	if err != nil {
		return err
	}
	go host.runObservation(ctx, catalog.SessionID, phase, method)
	return nil
}

func (p *SessionPolicy) runObservation(parent context.Context, sid, phase, method string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()

	// Observe at most once per interval. The module's data stays live between
	// decisions, and delayed RPCs never overlap ticks or advance twice.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastProblem string
	for ctx.Err() == nil {
		err := p.stepObservation(ctx, sid, phase, method)
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

func (p *SessionPolicy) stepObservation(ctx context.Context, sid, phase, method string) error {
	observed, err := p.manager.readPolicyObservation(ctx, sid, method)
	if err != nil {
		return err
	}
	sequence, err := p.Enqueue(behavior.Event{Kind: phase + "_observation", Value: observed})
	if err != nil {
		return err
	}
	_, _, err = p.Step(ctx, map[string]any{"kind": phase, "observationSequence": strconv.FormatUint(sequence, 10)}, nil)
	return err
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
