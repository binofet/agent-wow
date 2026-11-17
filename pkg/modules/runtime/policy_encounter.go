package modrt

import (
	"context"
	"strconv"

	modcb "github.com/agent-wow/agent-wow/pkg/modules/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const encounterObservationMethod = "buddy-actions.encounter_observation"

type policyEncounterVisitKey struct{}

type policyEncounterObservation struct {
	visit    uint64
	sequence string
}

func (m *Manager) renewPolicyEncounterLease(ctx context.Context, visit uint64) error {
	inst, _, err := m.lookup(actionsModule, "encounter_observation", true)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, policyObservationInterval)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	return modcb.RenewPolicyEncounterLease(ctx, inst.conn, inst.launch.Environment[modcb.PolicyEncounterKeyEnv], visit)
}

func (p *SessionPolicy) renewEncounterWriter(ctx context.Context, decision, action map[string]any, dispatch PolicyDispatch) error {
	current, observing := ctx.Value(policyEncounterVisitKey{}).(policyEncounterObservation)
	if !observing {
		return nil
	}
	hostile := dispatch.spell != nil && dispatch.spell.hostile()
	claim, _ := decision["encounterOwnership"].(map[string]any)
	visitID := strconv.FormatUint(current.visit, 10)
	if current.visit == 0 || claim["visitId"] != visitID || claim["observationSequence"] != current.sequence || hostile && action["visitId"] != visitID {
		if p.encounterLeaseHeld {
			if err := p.manager.renewPolicyEncounterLease(ctx, 0); err != nil {
				return err
			}
			p.encounterLeaseHeld = false
		}
		if hostile {
			return status.Error(codes.FailedPrecondition, "hostile policy action and ownership must match the current encounter observation")
		}
		return nil
	}
	if err := p.manager.renewPolicyEncounterLease(ctx, current.visit); err != nil {
		return err
	}
	p.encounterLeaseHeld = true
	if hostile {
		dispatch.spell.encounterVisit = current.visit
	}
	return nil
}

func (op *policySpell) hostile() bool {
	return op.capability == "offense" || op.capability == "polymorph" || op.capability == "sap"
}
