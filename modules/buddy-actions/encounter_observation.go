package buddyactions

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	partyv1 "github.com/agent-wow/agent-wow/modules/buddy-party/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"github.com/agent-wow/agent-wow/pkg/behavior"
	"github.com/agent-wow/agent-wow/pkg/channelstate"
	"github.com/agent-wow/agent-wow/pkg/movementflags"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"github.com/agent-wow/agent-wow/pkg/spellcatalog"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type encounterVisit struct {
	revision uint64
	mapID    uint32
	bound    bool
}

// World and Actions have separate packet queues. The same validated entry
// packets must advance both visit revisions before combining world and aura data.
func (s *Service) observeEncounterVisitLocked(op uint32, payload []byte) {
	switch op {
	case opcode.SMSGTransferPending:
		if len(payload) >= 4 {
			s.retireSurfaceGoalLocked()
			s.portalGoal = portalCatchupGoal{}
			s.encounterVisit.bound = false
			s.navigationGoalKnown = false
		}
	case opcode.SMSGLoginVerifyWorld, opcode.SMSGNewWorld:
		if !validEncounterWorldEntry(payload) {
			return
		}
		s.retireSurfaceGoalLocked()
		s.formation.Reset()
		s.followGoal = followFormationGoal{}
		s.portalGoal = portalCatchupGoal{}
		s.encounterVisit.revision++
		s.encounterVisit.mapID = binary.LittleEndian.Uint32(payload)
		s.encounterVisit.bound = true
		s.navigationGoalKnown = false
		s.state.BodyMovement = nil
		if op == opcode.SMSGLoginVerifyWorld {
			s.targetProtection.reset()
		}
	}
}

func validEncounterWorldEntry(payload []byte) bool {
	if len(payload) != 20 {
		return false
	}
	for offset := 4; offset < len(payload); offset += 4 {
		value := float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[offset:])))
		// Match buddy-world's position validation, including the NaN case.
		if !(math.Abs(value) < 1e6) {
			return false
		}
	}
	return true
}

// EncounterObservation projects this body's server observations. The Lua
// policy owns encounter bindings, ordering and decisions about dispatch.
func (s *Service) EncounterObservation(ctx context.Context, _ *emptypb.Empty) (*actionsv1.EncounterObservationResponse, error) {
	// Remote reads must not prevent the packet callback from advancing this
	// body's visit or completing an in-flight action. The callback connection
	// and independent mode are fixed at construction; capture identity under
	// the lock and validate the returned World against current native custody.
	s.mu.Lock()
	sessionID, selfGUID, closed := s.sessionID, s.selfGUID, s.closed
	transportSequence, supportOp, buffOp := s.transportSequence, s.supportOp, s.buffOp
	var supportView, buffView proto.Message
	if supportOp != nil {
		supportView = proto.Clone(&supportOp.view)
	}
	if buffOp != nil {
		buffView = proto.Clone(&buffOp.view)
	}
	s.mu.Unlock()
	var w *worldv1.SnapshotResponse
	var state *partyv1.StateResponse
	var readErr error
	if !closed {
		call, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		w, readErr = s.readWorldSnapshot(call)
		cancel()
		if readErr == nil {
			state = s.readPartyStateLocked(ctx)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Expiry is native operation bookkeeping, independent of inbound traffic.
	// Publish terminal receipts on this existing observation stream as well as
	// OnPacket, so a quiet world cannot leave Lua's action ledger pending.
	s.expireCrowdControlLocked()
	s.expireSelfChannelLocked()
	s.expireExplicitCastLocked()
	s.expireMeleeOperationLocked()
	s.supportSnapshotLocked(nil)
	s.buffTimeoutLocked()
	defer s.reportPolledPolicyEffectsLocked(ctx)

	identity := &actionsv1.EncounterObservationResponse{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, HostOwned: proto.Bool(false), HostRequested: proto.Bool(s.hostEncounterKey != ""),
	}
	if s.closed {
		identity.Reason = "session_closing"
		return identity, nil
	}
	if readErr != nil || ctx.Err() != nil || sessionID != s.sessionID || selfGUID != s.selfGUID {
		identity.Reason = "world_observation_unavailable"
		return identity, nil
	}
	if !s.matchesCurrentEncounterVisitLocked(w) {
		identity.Reason = "world_visit_unbound"
		return identity, nil
	}
	// If this exact operation receives GO during the read, already-observed
	// life must remain unattributed even on later reads of that retained fact.
	// Reuse normal death-custody settlement without claiming this cast's effect.
	if supportOp != nil && s.supportOp == supportOp &&
		!supportView.(*actionsv1.SpellOperation).ServerCastGo && supportOp.view.ServerCastGo && !supportOp.view.EffectObserved {
		s.settleResurrectionLifeLocked(w)
	}
	// Neither a new operation nor a receipt transition (for example GO)
	// may consume World evidence read before that transition. Packet handling
	// and its receipts still progress; only this composite is superseded.
	if s.transportSequence != transportSequence || s.supportOp != supportOp || s.buffOp != buffOp ||
		supportOp != nil && !proto.Equal(supportView, &supportOp.view) ||
		buffOp != nil && !proto.Equal(buffView, &buffOp.view) {
		identity.Reason = "world_observation_superseded"
		return identity, nil
	}
	s.observeRecoveryLifeLocked(w)

	out := &actionsv1.EncounterObservationResponse{
		SessionId: s.sessionID, SelfGuid: s.selfGUID, HostOwned: proto.Bool(s.hostEncounterActiveLocked()), HostRequested: identity.HostRequested,
		VisitId: w.VisitRevision, MapId: proto.Uint32(w.Self.MapId), WorldObservationRevision: w.ObservedPackets,
		GroundEffects: w.GroundEffects, GroundEffectsSupported: w.GroundEffectsSupported, GroundEffectsOverflow: w.GroundEffectsOverflow,
		Summoning: s.summoningObservationLocked(), SummoningObjects: w.SummoningObjects,
		Recovery:       s.recoveryStateLocked(),
		Interaction:    s.interactionObservationLocked(),
		SpellbookKnown: s.spellbookKnown, SpellbookRevision: s.spellbookRevision,
		Players:    make(map[string]*actionsv1.EncounterPlayer),
		Targets:    make(map[string]*actionsv1.EncounterCreature),
		Engagement: make(map[string]*actionsv1.EncounterEngagement),
		Spells:     make(map[string]*actionsv1.EncounterSpell),
	}
	out.Recovery.MotionPending = s.state.Moving || s.state.MotionUnconfirmed
	if s.state.LastTransportFailure != nil {
		out.LastTransportFailure = proto.Clone(s.state.LastTransportFailure).(*actionsv1.TransportFailure)
	}
	var online map[uint64]bool
	if state.GetSessionId() == s.sessionID {
		out.Party, online = s.encounterParty(state)
	}
	w = s.surfaceMovementWorldLocked(ctx, w)
	s.supportSnapshotLocked(w)
	s.encounterPlayers(w, online, out)
	out.TargetMovement = s.targetMovementSnapshotLocked()
	out.Navigation = &actionsv1.NavigationObservation{Independent: s.independent, MovementPointsSupported: true, MovementPoints: s.movementPointsStateLocked(), FollowRevision: s.followRevision, GroundNavigationConfigured: s.navDataDir != "", FollowFormationSupported: true, OwnerArrival: s.followNav.ownerArrival, FollowOwner: encounterFollowOwner(w.GetOwner()), BodyMovementSupported: true, BodyLandingSupported: true, Following: s.state.Following,
		MovementOwner: s.movementOwnerStateLocked(), OwnerGuid: w.GetOwner().GetGuid(), Phase: s.state.Reason, Detail: s.state.NavReason, RouteAvailable: s.state.NavAvailable,
		MotionUnconfirmed: s.state.MotionUnconfirmed, GoalKnown: s.navigationGoalKnown,
		GoalX: s.navigationGoal.x, GoalY: s.navigationGoal.y, GoalZ: s.navigationGoal.z}
	if s.state.BodyMovement != nil {
		out.Navigation.BodyMovement = proto.Clone(s.state.BodyMovement).(*actionsv1.BodyMovementState)
	}
	out.Navigation.Portal = s.portalObservationLocked(w)
	out.Navigation.GroundMount = s.followMountObservationLocked(ctx, w)
	out.Navigation.MovingSurface = s.surfaceObservationLocked(ctx, w)
	out.MeleeKnown = s.spellbookKnown && s.state.KnownAutoAttack && s.weaponReady(w.Self)
	if s.state.AutoAttacking && !s.state.AutoAttackUnconfirmed && s.meleeAcknowledgedVisit == w.VisitRevision {
		out.ActiveMeleeTargetGuid = s.state.AutoAttackTargetGuid
	}
	s.encounterCreatures(w, out)
	if out.Flightpath == nil && (s.flightpath.operation != nil || s.flightpathActivation.operation.GetPending()) {
		out.Flightpath = s.flightpathObservationLocked()
	}
	s.encounterSpells(w, online, out)
	out.TargetMovement = s.targetMovementSnapshotLocked()
	out.Support = s.encounterSupportLocked(ctx, w, online)

	// The policy event uses this same JSON representation. Refuse an
	// oversized composite view as a whole, since dropping arbitrary rows
	// could hide a target, observed victim or protection.
	body, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(out)
	if err != nil || len(body) > behavior.DefaultSessionValueBytes-512 {
		identity.Reason = "encounter_observation_bounds"
		return identity, nil
	}
	return out, nil
}

func (s *Service) encounterParty(state *partyv1.StateResponse) (*actionsv1.EncounterParty, map[uint64]bool) {
	group := state.GetGroup()
	if !group.GetKnown() {
		return nil, nil
	}
	if !group.InGroup {
		return &actionsv1.EncounterParty{Group: group, Revision: state.Revision}, nil
	}
	online := s.onlineHealingGroup(group)
	if online == nil || group.GroupGuid == 0 || group.LeaderGuid == 0 {
		return nil, nil
	}
	if _, found := online[group.LeaderGuid]; !found {
		return nil, nil
	}
	out := &actionsv1.EncounterParty{Group: group, Revision: state.Revision}
	if marks := state.RaidTargets; marks != nil && marks.GroupGuid == group.GroupGuid &&
		marks.Revision != 0 && marks.Revision <= state.Revision && marks.ListRevision <= marks.Revision && len(marks.Marks) <= 8 {
		out.RaidTargets = marks
	}
	return out, online
}

func encounterGUID(guid uint64) string {
	return strconv.FormatUint(guid, 10)
}

func (s *Service) encounterPlayers(w *worldv1.SnapshotResponse, online map[uint64]bool, out *actionsv1.EncounterObservationResponse) map[uint64]*worldv1.PlayerState {
	players := map[uint64]*worldv1.PlayerState{s.selfGUID: w.Self}
	for member, isOnline := range online {
		if member == s.selfGUID || !isOnline {
			continue
		}
		if p := observedHealingPlayer(w, member); p.GetPresent() && p.GetMapKnown() && p.MapId == w.Self.MapId {
			players[member] = p
		}
	}
	for _, patient := range w.NearbyPlayers {
		if patient.GetPresent() && patient.GetMapKnown() && patient.MapId == w.Self.MapId &&
			observedHealingPlayer(w, patient.Guid) == patient && s.healingAllyLocked(w, patient, online) {
			players[patient.Guid] = patient
		}
	}
	for guid, p := range players {
		view := &actionsv1.EncounterPlayer{
			Guid: guid, Present: p.Present, MapKnown: p.MapKnown, MapId: proto.Uint32(p.MapId),
			AliveKnown: p.AliveKnown, HealthKnown: p.HealthKnown, Health: p.Health, MaxHealth: p.MaxHealth,
			FactionTemplate: p.FactionTemplate, PlayerFlags: p.PlayerFlags,
			CombatReachKnown: p.CombatReachKnown, CombatReach: p.CombatReach,
			PositionKnown: p.PositionKnown && finite(playerPosition(p)),
			PositionAgeMs: p.PositionAgeMs, PositionAgeKnown: p.PositionKnown, PositionInferred: p.PositionInferred,
			ClassKnown: p.ClassKnown, ClassId: p.ClassId, RaceKnown: p.RaceKnown, RaceId: p.RaceId,
			LevelKnown: p.LevelKnown, Level: p.Level,
			PowerKnown: p.PowerKnown, PowerType: p.PowerType, Power: p.Power, MaxPower: p.MaxPower,
			MovementKnown: p.MovementFlagsKnown, Moving: p.MovementFlags&0xff != 0, Swimming: p.MovementFlagsKnown && p.MovementFlags&swimMovementFlag != 0,
			CombatKnown: p.UnitFlagsKnown, InCombat: p.UnitFlags&0x80000 != 0,
		}
		if view.PositionKnown {
			view.X, view.Y, view.Z, view.Orientation, view.PositionRevision = p.X, p.Y, p.Z, p.Orientation, p.PositionRevision
		}
		if guid == s.selfGUID && s.matchesCurrentEncounterVisitLocked(w) && p.Present && p.MapKnown && view.PositionKnown &&
			s.posKnown && finite(s.pos) && s.posMap == p.MapId && s.positionRevision == p.PositionRevision &&
			!s.state.MotionUnconfirmed && !s.clientControlLost && !s.clientControlRestoring {
			// Own movement is not echoed by the server. World retains the
			// raw baseline; Actions knows the pose it actually wrote against
			// that same map/revision. Lua must plan from that current pose.
			view.X, view.Y, view.Z, view.Orientation = s.pos.x, s.pos.y, s.pos.z, s.pos.o
			view.MovementKnown, view.Moving, view.Swimming = true, s.state.Moving, s.swimming
			current := s.clientPositionLocked()
			view.PositionAgeMs, view.PositionEstimated = current.AgeMs, current.Estimated
		}
		if p.AliveKnown {
			view.Alive = proto.Bool(p.Alive)
		}
		if p.TargetKnown {
			view.SelectedTargetGuid = proto.Uint64(p.TargetGuid)
		}
		if guid == s.selfGUID {
			view.ComboKnown, view.ComboTargetGuid, view.ComboPoints = s.weaponCombo.known, s.weaponCombo.target, s.weaponCombo.points
			if p.MovementFlagsKnown && movementflags.GroundCompatible(p.MovementFlags, p.ExtraMovementFlags) &&
				!s.swimming && !s.clientControlLost && !s.clientControlRestoring && observedRunSpeed(p) > 0 {
				view.GroundRunSpeed = proto.Float32(p.RunSpeed)
			}
		}
		out.Players[encounterGUID(guid)] = view
	}
	leader := players[out.GetParty().GetGroup().GetLeaderGuid()]
	if leader.GetTargetKnown() {
		out.ObservedOwnerFocus = leader.TargetGuid
	}
	return players
}

func (s *Service) encounterCreatures(w *worldv1.SnapshotResponse, out *actionsv1.EncounterObservationResponse) {
	if len(w.NearbyCreatures) > 32 {
		return
	}
	out.NearbyCreaturesComplete = w.NearbyCreaturesComplete
	// This advertises admission to start autoattack, not physical hit readiness.
	// Current positions/orientations remain available to Lua for approach/chase.
	meleeCaster := out.MeleeKnown && s.exactMeleeCasterObservedLocked(w)
	extra := []*worldv1.CreatureState{w.OwnerTarget}
	if selected := w.SelectedCreatureOutsideNearby; selected != nil && w.GetSelf().GetTargetKnown() && selected.Guid == w.GetSelf().GetTargetGuid() {
		extra = append(extra, selected)
	}
	for _, rows := range [][]*worldv1.CreatureState{w.NearbyCreatures, extra} {
		for _, c := range rows {
			if c == nil || !c.Present {
				continue
			}
			if !c.MapKnown || c.Guid>>48 != 0xf130 || c.Guid&0xffffff == 0 {
				out.NearbyCreaturesComplete = false
				continue
			}
			if c.MapId != w.Self.MapId {
				continue
			}
			key := encounterGUID(c.Guid)
			if out.Targets[key] != nil {
				continue
			}
			view := &actionsv1.EncounterCreature{
				Guid: c.Guid, Entry: c.Entry, Present: c.Present, MapKnown: c.MapKnown, MapId: proto.Uint32(c.MapId),
				AliveKnown: c.AliveKnown, HealthKnown: c.HealthKnown, Health: c.Health, MaxHealth: c.MaxHealth,
				Attackable: c.AliveKnown && c.Alive && c.HealthKnown && c.Health != 0 && c.UnitFlagsKnown && c.DynamicFlagsKnown &&
					c.UnitFlags&(0x2|0x80|0x100|0x10000|0x2000000|0x80000000) == 0 && c.UnitFlags_2&1 == 0 && c.DynamicFlags&0x20 == 0,
				UnitFlagsKnown: c.UnitFlagsKnown, UnitFlags: c.UnitFlags, FactionTemplate: c.FactionTemplate,
				AttackObservation: &actionsv1.EncounterAttackObservation{VictimKnown: c.AttackVictimKnown,
					VictimGuid: c.AttackVictimGuid, AgeMs: c.AttackAgeMs, TargetKnown: c.TargetKnown, TargetGuid: c.TargetGuid},
				PositionKnown: c.PositionKnown && finite(position{c.X, c.Y, c.Z, c.Orientation}),
				Trajectory:    c.Trajectory, ActiveCast: c.ActiveCast, VisibleAuras: c.VisibleAuras, LevelKnown: c.LevelKnown,
				ObservationAgeMs: c.ObservationAgeMs, FactionTemplateKnown: c.FactionTemplateKnown,
				OwnDamageFamilies: s.encounterOwnDamageFamilies(c.Guid),
			}
			if view.PositionKnown {
				view.X, view.Y, view.Z, view.Orientation, view.PositionRevision = c.X, c.Y, c.Z, c.Orientation, c.PositionRevision
				view.PositionAgeMs = proto.Uint64(c.PositionAgeMs)
			}
			if c.LevelKnown {
				view.Level = proto.Uint32(c.Level)
			}
			if c.NpcFlagsKnown {
				view.NpcFlags = proto.Uint32(c.NpcFlags)
			}
			if c.UnitFlags_2Known {
				view.UnitFlags_2 = proto.Uint32(c.UnitFlags_2)
			}
			if c.DynamicFlagsKnown {
				view.DynamicFlags = proto.Uint32(c.DynamicFlags)
			}
			if c.MovementFlagsKnown {
				view.MovementFlags = proto.Uint32(c.MovementFlags)
			}
			if c.DistanceToSelfObservedKnown {
				view.DistanceToSelfObserved = proto.Float32(c.DistanceToSelfObserved)
			}
			if c.DistanceToOwnerObservedKnown {
				view.DistanceToOwnerObserved = proto.Float32(c.DistanceToOwnerObserved)
			}
			if tooltip := s.creatureTooltips.entries[c.Entry]; tooltip != nil && uint32(c.Guid>>24)&0xffffff == c.Entry {
				if out.CreatureTemplates == nil {
					out.CreatureTemplates = make(map[string]*actionsv1.EncounterCreatureTemplate)
				}
				view.TemplateEntry = proto.Uint32(c.Entry)
				out.CreatureTemplates[strconv.FormatUint(uint64(c.Entry), 10)] = tooltip
			}
			if c.NpcFlags&0x2000 != 0 {
				view.Flightmaster = &actionsv1.EncounterFlightmaster{StationaryPositionKnown: c.StationaryPositionKnown,
					PositionAgeMs: c.PositionAgeMs, ObservationAgeMs: c.ObservationAgeMs, MovementFlags: c.MovementFlags,
					FactionTemplateFriendly: s.flightmasterFriendlyLocked(w.Self.FactionTemplate, c.FactionTemplate)}
				if out.Flightpath == nil {
					out.Flightpath = s.flightpathObservationLocked()
				}
			}
			if c.CombatReachKnown {
				view.CombatReach = proto.Float32(c.CombatReach)
			}
			if c.AliveKnown {
				view.Alive = proto.Bool(c.Alive)
			}
			view.MeleeReady = view.Attackable && meleeCaster && !s.autoStopping &&
				!s.state.AutoAttackUnconfirmed && s.pendingMeleeOperationLocked() == nil &&
				s.exactMeleeStartAvailableLocked(c.Guid) && exactOffenseTarget(w, c, c.Guid, nil)
			out.Targets[key] = view
		}
	}
	protection := &actionsv1.State{}
	s.targetProtection.snapshot(protection, s.now())
	out.TargetProtectionOverflow = protection.TargetProtectionOverflow
	for _, aura := range protection.ProtectedTargets {
		if c := out.Targets[encounterGUID(aura.TargetGuid)]; c != nil {
			c.Protected = true
			out.ProtectedAuras = append(out.ProtectedAuras, aura)
		}
	}
}

func (s *Service) encounterSpells(w *worldv1.SnapshotResponse, online map[uint64]bool, out *actionsv1.EncounterObservationResponse) {
	out.Healing = s.healingOptionsLocked(w, func() map[uint64]bool { return online })
	heals := make(map[uint32]bool, len(out.Healing.Spells))
	for _, rank := range out.Healing.Spells {
		heals[rank.SpellId] = true
	}
	if !s.spellbookKnown {
		return
	}
	// Aura classification uses the same lazily loaded catalog as these ranks.
	// Initialize it before the first caster-readiness projection, including
	// rogues whose non-mana healing view does not enter learnedContractsLocked.
	contracts := s.learnedContractsLocked()
	self := w.Self
	currentCaster := safeSelfCombat(w, s.selfGUID) == ""
	controlCaster := currentCaster && s.crowdControlIdleLocked() &&
		self.ClassKnown && self.MovementFlags&0xff == 0 && channelstate.Idle(self)
	polyCaster := controlCaster && self.ClassId == 8
	sapCaster := controlCaster && self.ClassId == 4 && self.UnitFlags&0x80000 == 0 &&
		!s.questApproaching && !s.clientControlLost &&
		!s.clientControlRestoring && s.stealthedLocked()
	polyReady, sapReady := false, false
	for id, spell := range contracts {
		view := encounterSpellMechanics(spell, self)
		view.GroundMountSpeedPercent = s.spellCatalogLocked().GroundMountSpeed(id)
		view.SelfAuraKnown, view.SelfAuraActive = s.encounterSelfAura(spell.Family)
		switch spell.Capability().Kind {
		case spellcatalog.Buff:
			view.Capability = "buff"
			view.Available = s.buffRankAvailableLocked(self, spell)
			view.Ready = view.Available && s.buffCasterAvailableLocked(w, spell)
		case spellcatalog.Heal:
			view.Available = s.healRankAvailableLocked(self, spell)
			view.Ready = out.Healing.Caster.Ready && heals[id]
			view.Capability = "heal"
		case spellcatalog.Polymorph:
			view.Available = self.PowerKnown && int32(self.PowerType) == spell.Power &&
				uint64(self.Power) >= spellCost(spell, self) && !s.now().Before(s.spellReady[id]) && casterRequirementsObserved(spell, self)
			view.Ready = polyCaster && view.Available
			view.Capability = "polymorph"
			polyReady = polyReady || view.Ready
		case spellcatalog.Sap:
			view.Capability = "sap"
			// The exact cast still checks the selected NPC's observed type and range.
			view.Available = self.PowerKnown && int32(self.PowerType) == spell.Power &&
				uint64(self.Power) >= spellCost(spell, self) && !s.spellOnCooldownLocked(spell) &&
				(spell.ItemClass < 0 || s.weaponReady(self)) && casterRequirementsObserved(spell, self)
			view.Ready = sapCaster && view.Available
			sapReady = sapReady || view.Ready
		case spellcatalog.Damage, spellcatalog.WeaponStrike:
			if !s.offenseRankSupportedLocked(spell) {
				// Expose learned mechanics even when an automatic offense
				// classifier has no handler. Lua may use the normal generic
				// cast contract; this creates no new packet authority.
				view.Capability = "generic"
				view.Available = s.encounterRankResourcesAvailable(spell, self)
				view.Ready = currentCaster && !s.state.CastPending && view.Available
				break
			}
			view.Capability = "offense"
			if instantWeaponStrike(spell) {
				s.reflectWeaponStrike(view, spell, self)
			}
			view.Available = s.offenseRankReadyLocked(self, spell)
			view.Ready = currentCaster && s.offenseCustodyFor(spell) && s.offenseCasterFor(self, spell) && view.Available
		case spellcatalog.Stealth:
			if !view.SelfCastSupported {
				continue
			}
			view.Capability = "stealth"
			view.Available = self.UnitFlagsKnown && self.UnitFlags&0x80000 == 0 && s.encounterRankResourcesAvailable(spell, self)
			view.Ready = currentCaster && !s.selfCastBusyLocked() && self.MovementFlagsKnown && self.MovementFlags&0xff == 0 && view.Available
		case spellcatalog.Resurrect, spellcatalog.Interrupt:
			view.Capability = string(spell.Capability().Kind)
			view.Available = s.encounterRankResourcesAvailable(spell, self)
			// Exact patient/enemy-cast readiness is reflected in support options.
		case spellcatalog.Cleanse:
			view.Capability = "cleanse"
			view.FriendlyDispelMask = spell.FriendlyDispelMask()
			view.Available = s.cleanseRankAvailableLocked(self, spell)
			view.Ready = view.Available && s.cleanseCasterAvailableLocked(w, spell)
		default:
			if spell.Passive() {
				continue
			}
			view.Capability = "generic"
			view.Available = s.encounterRankResourcesAvailable(spell, self)
			view.Ready = currentCaster && !s.state.CastPending && view.Available
		}
		out.Spells[encounterGUID(uint64(id))] = view
	}
	if polyReady {
		out.ControlCapabilities = append(out.ControlCapabilities, &actionsv1.EncounterControlCapability{Guid: s.selfGUID, Technique: "polymorph", Ready: true})
	}
	if sapReady {
		out.ControlCapabilities = append(out.ControlCapabilities, &actionsv1.EncounterControlCapability{Guid: s.selfGUID, Technique: "sap", Ready: true})
	}
}

// No game packets are sent here. Tokens and native phase parsers correlate the
// same terminal receipts consumed by the packet callback path exactly once.
func (s *Service) reportPolledPolicyEffectsLocked(ctx context.Context) {
	if op := s.buffOp; op != nil && op.policySpellToken != "" {
		token := op.policySpellToken
		op.reportPolicySpellEffect(ctx)
		op.policySpellToken = token
	}
	if op := s.supportOp; op != nil && op.policySpellToken != "" {
		// Keep the native token until a packet callback/new operation retires
		// it. A canceled observation read may lose its trailers; repeating a
		// token is harmless because the host consumes each action once.
		token := op.policySpellToken
		op.reportPolicySpellEffect(ctx, s.state.CastPending)
		op.policySpellToken = token
	}
	if op := s.meleeOp; op != nil && op.token != "" {
		token := op.token
		op.reportPolicyMeleeEffect(ctx)
		op.token = token
	}
}

// The followed owner may be outside the group. These are raw observed facts,
// independent of the party authority used for combat and support.
func encounterFollowOwner(p *worldv1.PlayerState) *actionsv1.EncounterPlayer {
	if p == nil {
		return nil
	}
	out := &actionsv1.EncounterPlayer{Guid: p.Guid, Present: p.Present, MapKnown: p.MapKnown,
		MapId: proto.Uint32(p.MapId), AliveKnown: p.AliveKnown, PositionKnown: p.PositionKnown && finite(playerPosition(p)),
		PositionRevision: p.PositionRevision, X: p.X, Y: p.Y, Z: p.Z, Orientation: p.Orientation,
		PositionAgeMs: p.PositionAgeMs, PositionAgeKnown: p.PositionKnown, PositionInferred: p.PositionInferred,
		MovementKnown: p.MovementFlagsKnown, Moving: p.MovementFlags&0x3f != 0, Swimming: p.MovementFlagsKnown && p.MovementFlags&swimMovementFlag != 0, PlayerFlags: p.PlayerFlags,
		CombatKnown: p.UnitFlagsKnown, InCombat: p.UnitFlags&0x80000 != 0}
	if p.AliveKnown {
		out.Alive = proto.Bool(p.Alive)
	}
	return out
}
