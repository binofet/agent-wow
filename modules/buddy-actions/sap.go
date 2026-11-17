package buddyactions

import (
	"context"
	"encoding/binary"
	"time"

	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/creaturequery"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type controlTemplate struct {
	creatureType uint32
	requestedAt  time.Time
}

func (s *Service) sapReadyLocked(ctx context.Context, w *worldv1.SnapshotResponse, spell spellcatalog.Spell) (bool, string, error) {
	return s.sapReadinessLocked(ctx, w, spell, true)
}

func (s *Service) sapReadinessLocked(ctx context.Context, w *worldv1.SnapshotResponse, spell spellcatalog.Spell, allowQueries bool) (bool, string, error) {
	self, target := w.Self, w.RequestedCreature
	if self.UnitFlags&0x80000 != 0 {
		return false, "sap_caster_in_combat", status.Error(codes.FailedPrecondition, "out-of-combat rogue and exact out-of-combat creature required")
	}
	if target.UnitFlags&0x80000 != 0 {
		return false, "sap_target_in_combat", status.Error(codes.FailedPrecondition, "out-of-combat rogue and exact out-of-combat creature required")
	}
	if target.Entry == 0 || uint32(target.Guid>>24&0xffffff) != target.Entry {
		return false, "sap_target_identity_changed", status.Error(codes.FailedPrecondition, "out-of-combat rogue and exact out-of-combat creature required")
	}
	if !s.stealthedLocked() && len(s.candidatesLocked(spellcatalog.Stealth)) == 0 {
		return false, "sap_stealth_unavailable", status.Error(codes.FailedPrecondition, "observed or available learned Stealth required")
	}
	if spell.ItemClass >= 0 && !s.weaponReady(self) {
		if !allowQueries {
			return false, "sap_weapon_not_confirmed", status.Error(codes.FailedPrecondition, "policy Sap requires a verified equipped weapon")
		}
		s.requestWeaponLocked(ctx, self)
		s.state.Reason = "sap_weapon_not_confirmed"
		return false, s.state.Reason, nil
	}
	now := s.now()
	s.targetProtection.expire(now)
	for _, aura := range s.targetProtection.auras {
		if aura.caster != s.selfGUID {
			continue
		}
		current, ok := spellcatalog.Default().Lookup(aura.spell)
		if ok && current.Capability().Kind == spellcatalog.Sap {
			return false, "sap_already_active", status.Error(codes.FailedPrecondition, "this rogue already has an observed Sap target")
		}
	}
	proof := s.controlTemplates[target.Entry]
	switch proof.creatureType {
	case 1, 2, 3, 7: // Beast, dragonkin, demon, humanoid.
		return true, "", nil
	case 0:
		if !allowQueries {
			return false, "sap_target_type_pending", status.Error(codes.FailedPrecondition, "policy Sap requires an observed eligible creature type")
		}
		s.queryControlTemplateLocked(ctx, target, now)
		s.state.Reason = "sap_target_type_pending"
		return false, s.state.Reason, nil
	default:
		return false, "sap_target_type_ineligible", status.Error(codes.FailedPrecondition, "observed creature type is not eligible for Sap")
	}
}

// Enter ordinary self Stealth without advancing or attacking the selected
// creature. The next explicit Sap still needs current target and stance proof.
func (s *Service) prepareSapStealthLocked(ctx context.Context, target uint64) error {
	for _, r := range s.candidatesLocked(spellcatalog.Stealth) {
		if s.now().Before(s.spellReady[r.ID]) {
			continue
		}
		s.castSupportLocked(ctx, r, s.selfGUID, target)
		s.state.Reason = "sap_waiting_for_stealth"
		if !s.supportOp.view.TransportWritten {
			s.state.Reason = "sap_stealth_transport_unconfirmed"
		}
		return nil
	}
	return status.Error(codes.FailedPrecondition, "learned Stealth is not currently available")
}

func (s *Service) queryControlTemplateLocked(ctx context.Context, target *worldv1.CreatureState, now time.Time) {
	if current, ok := s.controlTemplates[target.Entry]; ok && now.Sub(current.requestedAt) < 2*time.Second {
		return
	}
	if len(s.controlTemplates) >= 64 {
		var oldest uint32
		var at time.Time
		for entry, row := range s.controlTemplates {
			if oldest == 0 || row.requestedAt.Before(at) {
				oldest, at = entry, row.requestedAt
			}
		}
		delete(s.controlTemplates, oldest)
	}
	if s.controlTemplates == nil {
		s.controlTemplates = make(map[uint32]controlTemplate)
	}
	s.controlTemplates[target.Entry] = controlTemplate{requestedAt: now}
	s.sendLocked(ctx, opcode.CMSGCreatureQuery, creaturequery.Request(target.Entry, target.Guid))
}

func (s *Service) observeControlTemplateLocked(payload []byte) {
	if len(payload) < 4 || s.closed {
		return
	}
	entry := binary.LittleEndian.Uint32(payload) & 0x7fffffff
	proof, requested := s.controlTemplates[entry]
	if !requested {
		return
	}
	template, err := creaturequery.Decode(payload)
	if err != nil {
		if len(payload) != 4 || binary.LittleEndian.Uint32(payload)&0x80000000 == 0 {
			s.state.MalformedPackets++
		}
		return
	}
	proof.creatureType = template.CreatureType
	s.controlTemplates[entry] = proof
}
