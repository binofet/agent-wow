// Package buddyprofessions implements finite own-character starter practice.
package buddyprofessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"

	actionsv1 "github.com/agent-wow/agent-wow/modules/buddy-actions/api"
	lootv1 "github.com/agent-wow/agent-wow/modules/buddy-loot/api"
	professionsv1 "github.com/agent-wow/agent-wow/modules/buddy-professions/api"
	tradev1 "github.com/agent-wow/agent-wow/modules/buddy-trade/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	modv1 "github.com/agent-wow/agent-wow/pkg/modules/v1"
	"github.com/agent-wow/agent-wow/pkg/opcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

const limitation = "One bounded own-known starter recipe, fishing attempt or explicitly selected already-in-range skinning corpse or herbalism object, or one explicitly selected carried item for disenchanting. Unconfirmed disenchanting materials require this companion to remain connected; never close its material loot window. Transport, server cast, inventory output and pure skill-rank increase are separate evidence. Focus distance may use the stopped client's position estimate, which is not a server movement acknowledgement. Missing supplies, equipment, focus or observations block practice. No movement, buying, equipping, unlearning or level progression."

var requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type sessionAPI interface {
	SendPacket(context.Context, *modv1.SendPacketRequest, ...grpc.CallOption) (*emptypb.Empty, error)
	InvokeModule(context.Context, *modv1.InvokeModuleRequest, ...grpc.CallOption) (*modv1.InvokeModuleResponse, error)
}

type operation struct {
	request                                                     *professionsv1.AttemptRequest
	receipt                                                     *professionsv1.Operation
	spec                                                        recipeSpec
	baseline                                                    worldView
	token                                                       string
	started                                                     time.Time
	ctx                                                         context.Context
	cancel                                                      context.CancelFunc
	done                                                        chan struct{}
	castReserved, reelReserved, releaseReserved, cancelReserved bool
	window                                                      *lootWindow
	windowInvalid                                               bool
	stored                                                      map[byte]bool
	removed                                                     map[byte]bool
	pushed                                                      map[byte]bool
	pushCounts                                                  map[uint32]uint32
}

type Service struct {
	professionsv1.UnimplementedBuddyProfessionsServer
	mu                     sync.Mutex
	observeGate            chan struct{}
	session                sessionAPI
	selfGUID               uint64
	now                    func() time.Time
	state                  *professionsv1.State
	book                   spellBook
	view                   worldView
	viewKnown              bool
	viewReason             string
	actions                *actionsv1.State
	loot                   *lootv1.State
	tools                  map[uint32]*tradev1.ItemDetails
	focuses                map[uint32]focusTemplate
	focusRequested         map[uint32]bool
	skinLoot               *normalLootProof
	fullLoot               map[uint64]time.Time
	skinAttempted          map[uint64]bool
	skinTemplates          map[uint32]skinTemplate
	skinRequested          map[uint32]bool
	herbTemplates          map[uint32]herbTemplate
	herbRequested          map[uint32]bool
	herbAttempted          map[uint64]bool
	selectedHerbGUID       uint64
	deTemplates            map[uint32]disenchantTemplate
	deRequested            map[uint32]bool
	deAttempted            map[uint64]bool
	selectedDisenchantGUID uint64
	pendingDisenchant      *operation
	deTrade                *tradev1.State
	nextQuery              time.Time
	requests               map[string]*operation
	active, last           *operation
	castCount              byte
	closed                 bool
}

func NewService(session sessionAPI, sessionID string, selfGUID uint64) *Service {
	return &Service{session: session, selfGUID: selfGUID, now: time.Now,
		observeGate: make(chan struct{}, 1),
		state:       &professionsv1.State{SessionId: sessionID, SelfGuid: selfGUID, Revision: 1, Limitation: limitation},
		tools:       make(map[uint32]*tradev1.ItemDetails), focuses: make(map[uint32]focusTemplate),
		focusRequested: make(map[uint32]bool), fullLoot: make(map[uint64]time.Time), skinAttempted: make(map[uint64]bool), skinTemplates: make(map[uint32]skinTemplate), skinRequested: make(map[uint32]bool), herbTemplates: make(map[uint32]herbTemplate), herbRequested: make(map[uint32]bool), herbAttempted: make(map[uint64]bool), deTemplates: make(map[uint32]disenchantTemplate), deRequested: make(map[uint32]bool), deAttempted: make(map[uint64]bool), requests: make(map[string]*operation)}
}

func (s *Service) invoke(ctx context.Context, module, method string, input, output proto.Message) error {
	if s.session == nil {
		return status.Error(codes.Unavailable, "session callback unavailable")
	}
	raw, err := anypb.New(input)
	if err != nil {
		return err
	}
	result, err := s.session.InvokeModule(ctx, &modv1.InvokeModuleRequest{Module: module, Method: method, Request: raw})
	if err != nil {
		return err
	}
	if result == nil || result.Result == nil {
		return status.Error(codes.Unavailable, "module result missing")
	}
	return result.Result.UnmarshalTo(output)
}

func worldObjects(w *worldv1.SnapshotResponse, name string) []*worldv1.GameObjectState {
	if w == nil {
		return nil
	}
	if name == "spell_focuses" && len(w.SpellFocuses) <= 32 {
		return w.SpellFocuses
	}
	if name == "own_fishing_bobbers" && len(w.OwnFishingBobbers) <= 4 {
		return w.OwnFishingBobbers
	}
	return nil
}

// Read normal public template facts only for currently observed focus/tool IDs.
// Queries are bounded and reserved before transport; they are not craft receipts.
func (s *Service) observe(ctx context.Context, target ...uint64) error {
	select {
	case s.observeGate <- struct{}{}:
		defer func() { <-s.observeGate }()
	case <-ctx.Done():
		s.invalidateObservations(ctx.Err())
		return ctx.Err()
	}
	s.mu.Lock()
	targetGUID := s.selectedHerbGUID
	if s.selectedDisenchantGUID != 0 {
		targetGUID = s.selectedDisenchantGUID
	}
	needDE := s.selectedDisenchantGUID != 0 || s.pendingDisenchant != nil || s.active != nil && s.active.spec.skill == 333
	if s.active != nil && (s.active.spec.skill == 393 || s.active.spec.skill == 182 || s.active.spec.skill == 333) && s.active.request != nil {
		targetGUID = s.active.request.TargetGuid
	}
	if len(target) == 1 {
		targetGUID = target[0]
	}
	s.mu.Unlock()
	request := &worldv1.SnapshotRequest{}
	if targetGUID>>48 == 0xf110 {
		request.GameObjectEntry = uint32(targetGUID>>24) & 0xffffff
	} else if targetGUID>>48 == 0xf130 {
		request.CreatureGuid = targetGUID
	}
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", request, w); err != nil {
		s.invalidateObservations(err)
		return err
	}
	view, err := validateWorld(w, s.selfGUID)
	a, l := &actionsv1.State{}, &lootv1.State{}
	if err == nil {
		err = s.invoke(ctx, "buddy-actions", "status", &emptypb.Empty{}, a)
	}
	if err == nil {
		err = s.invoke(ctx, "buddy-loot", "state", &emptypb.Empty{}, l)
	}
	var deTrade *tradev1.State
	if err == nil && needDE {
		deTrade = &tradev1.State{}
		err = s.invoke(ctx, "buddy-trade", "state", &emptypb.Empty{}, deTrade)
		if err == nil && deTrade.SessionId != s.state.SessionId {
			err = fail("actual_trade_session_mismatch")
		}
	}
	s.mu.Lock()
	if needDE {
		s.deTrade = deTrade
	}
	if err == nil && (view.skillsRevision < s.state.SkillsRevision || view.inventoryRevision < s.state.InventoryRevision) {
		err = fail("actual_world_revision_regressed")
	}
	oldSkills, oldBag, oldReason := s.state.SkillsRevision, s.state.InventoryRevision, s.viewReason
	s.viewKnown, s.viewReason = err == nil, ""
	if err != nil {
		s.viewReason = err.Error()
	} else {
		s.view, s.actions, s.loot = view, a, l
		s.state.SkillsKnown, s.state.SkillsRevision = true, view.skillsRevision
		s.state.BackpackKnown, s.state.BackpackRevision = w.BackpackKnown, view.backpackRevision
		s.state.InventoryKnown, s.state.InventoryRevision = true, view.inventoryRevision
		s.state.Skills = nil
		for _, id := range []uint32{129, 185, 356, 393, 182, 333} {
			if rank, ok := view.skills[id]; ok {
				s.state.Skills = append(s.state.Skills, &professionsv1.Skill{SkillId: id, Rank: rank.value, Maximum: rank.max})
			}
		}
		s.updateEvidenceLocked(view)
	}
	if err != nil {
		s.state.SkillsKnown = false
		s.state.BackpackKnown = false
		s.state.InventoryKnown = false
	}
	if oldSkills != s.state.SkillsRevision || oldBag != s.state.InventoryRevision || oldReason != s.viewReason {
		s.state.Revision++
	}
	query := []*worldv1.GameObjectState{}
	if err == nil && !s.now().Before(s.nextQuery) {
		for _, object := range worldObjects(w, "spell_focuses") {
			if len(query) >= 4 || len(s.focusRequested) >= 32 {
				break
			}
			if object != nil && object.Present && object.TypeKnown && object.GameObjectType == 8 && object.Entry > 0 && !s.focusRequested[object.Entry] {
				s.focusRequested[object.Entry] = true
				query = append(query, object)
			}
		}
		if len(query) > 0 {
			s.nextQuery = s.now().Add(time.Second)
		}
	}
	var toolEntry uint32
	if err == nil && view.self.Mainhand != nil && view.self.Mainhand.SlotKnown && view.self.Mainhand.MetadataKnown {
		toolEntry = view.self.Mainhand.Entry
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, object := range query {
		if _, e := s.session.SendPacket(ctx, &modv1.SendPacketRequest{Opcode: opcode.CMSGGameobjectQuery, Payload: focusQueryPacket(object.Entry, object.Guid)}); e == nil {
			s.mu.Lock()
			s.state.TransportWrites++
			s.mu.Unlock()
		}
	}
	if toolEntry > 0 {
		s.mu.Lock()
		known := s.tools[toolEntry] != nil
		s.mu.Unlock()
		if !known {
			result := &tradev1.LookupItemsResponse{}
			if e := s.invoke(ctx, "buddy-trade", "lookup_items", &tradev1.LookupItemsRequest{ItemIds: []uint32{toolEntry}}, result); e == nil && result.SessionId == s.state.SessionId {
				s.mu.Lock()
				for _, item := range result.Items {
					if item != nil && item.ItemId == toolEntry && item.Available {
						s.tools[toolEntry] = proto.Clone(item).(*tradev1.ItemDetails)
						s.state.Revision++
					}
				}
				s.mu.Unlock()
			}
		}
	}
	s.querySkinTemplates(ctx, w)
	s.queryHerbTemplates(ctx, w)
	if needDE {
		s.queryDisenchantTemplates(ctx, w)
	}
	return nil
}

func (s *Service) invalidateObservations(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewKnown = false
	s.state.SkillsKnown = false
	s.state.BackpackKnown = false
	s.state.InventoryKnown = false
	if err != nil {
		s.viewReason = err.Error()
	} else {
		s.viewReason = "own_observations_unavailable"
	}
	s.state.Revision++
}

func (s *Service) reasonLocked(recipe recipeSpec) string {
	return s.readinessLocked(recipe, nil)
}
func (s *Service) readinessLocked(recipe recipeSpec, own *operation) string {
	if s.closed {
		return "session_closing"
	}
	if !s.viewKnown {
		if s.viewReason != "" {
			return s.viewReason
		}
		return "own_observations_unavailable"
	}
	if !s.book.known {
		return "complete_own_spellbook_unavailable"
	}
	if !s.book.has(recipe.spell) {
		return "recipe_not_observed_in_own_spellbook"
	}
	rank, ok := s.view.skills[recipe.skill]
	if !ok || rank.value < recipe.minimumSkill {
		return "profession_skill_not_learned_or_rank_too_low"
	}
	if s.view.freeSlots == 0 {
		return "no_verified_general_carried_inventory_slot"
	}
	if s.active != nil && s.active != own {
		return "another_profession_attempt_active"
	}
	if s.last != nil && (s.last.receipt.CleanupUnconfirmed || s.last.receipt.Status == professionsv1.AttemptStatus_OUTCOME_UNCONFIRMED) {
		return "previous_attempt_requires_reconciliation"
	}
	if s.loot == nil || s.loot.Enabled || s.loot.PendingCorpseGuid != 0 || s.loot.PendingGameObjectGuid != 0 || (s.loot.Phase != "" && s.loot.Phase != "idle" && s.loot.Phase != "disabled") {
		return "loot_controller_or_window_active"
	}
	if s.actions == nil || s.actions.Moving || s.actions.Traveling || s.actions.Practicing || s.actions.CastPending || s.actions.MotionUnconfirmed || s.actions.AutoAttacking || s.actions.AutoAttackRequested || s.actions.AutoAttackUnconfirmed || (s.actions.InteractionOwner != "" && !(own != nil && s.actions.InteractionOwner == "professions")) {
		return "controller_not_idle"
	}
	if owner := s.view.raw.Owner; owner != nil && owner.Present && owner.UnitFlagsKnown && owner.UnitFlags&0x80000 != 0 {
		return "observed_owner_combat_takes_priority"
	}
	if recipe.reagentEntry != 0 && s.view.counts[recipe.reagentEntry] < recipe.reagentCount {
		return "required_materials_not_observed_in_own_carried_inventory"
	}
	if recipe.skill == 393 || recipe.skill == 182 || recipe.skill == 333 {
		if recipe.skill == 393 && s.view.counts[7005] == 0 {
			return "actual_owned_skinning_knife7005_required"
		}
		if !s.view.self.ChannelSpellKnown || !s.view.self.ChannelObjectKnown || s.view.self.ChannelSpellId != 0 || s.view.self.ChannelObjectGuid != 0 {
			return "own_channel_not_observed_idle"
		}
	}
	if recipe.skill == 333 {
		if s.state.StopPrepared {
			return "normal_stop_prepared_no_new_disenchant_this_session"
		}
		if s.actions.Following || s.actions.Healing || s.actions.Assisting || s.actions.FollowLeaseRemainingMs != 0 || s.actions.HealingLeaseRemainingMs != 0 || s.actions.AssistLeaseRemainingMs != 0 {
			return "disable_follow_heal_and_assist_before_selected_item_disenchanting"
		}
		t := s.deTrade
		if t == nil || t.SessionId != s.state.SessionId || t.Phase != "IDLE" || t.PartnerGuid != 0 || t.PartnerAccepted || t.AcceptTransportWritten || t.JunkDisposalEnabled || t.LastJunkDisposal != nil && t.LastJunkDisposal.Status != "SERVER_SLOT_REMOVAL_CONFIRMED" {
			return "actual_idle_trade_and_junk_state_required_for_disenchanting"
		}
	}
	if recipe.skill == 356 {
		form := s.view.self.ShapeshiftForm
		if !s.view.self.ShapeshiftFormKnown || form > 32 {
			return "actual_own_shapeshift_form_unavailable"
		}
		if form > 0 && recipe.stanceNotMask&(uint32(1)<<(uint32(form)-1)) != 0 {
			return "current_observed_form_excludes_fishing"
		}
		if !recipe.channelDurationKnown {
			return "fishing_channel_duration_unknown"
		}
		if !s.view.self.ChannelSpellKnown || !s.view.self.ChannelObjectKnown || s.view.self.ChannelSpellId != 0 || s.view.self.ChannelObjectGuid != 0 {
			return "own_channel_not_observed_idle"
		}
		hand := s.view.self.Mainhand
		if hand == nil || !hand.SlotKnown || hand.ItemGuid == 0 || !hand.MetadataKnown || !hand.DurabilityKnown || hand.MaxDurability > 0 && hand.Durability == 0 {
			return "equipped_fishing_pole_not_observed; vendor_and_equip_capability_required"
		}
		item := s.tools[hand.Entry]
		if item == nil || !item.Available {
			return "equipped_tool_public_metadata_unavailable"
		}
		if item.ItemClass != uint32(recipe.itemClass) || item.Subclass >= 32 || recipe.subclassMask&(uint32(1)<<item.Subclass) == 0 {
			return "current_mainhand_is_not_a_verified_fishing_pole; vendor_and_equip_capability_required"
		}
		if item.RequiredLevel > s.view.level || item.RequiredSpell != 0 || item.RequiredReputationFaction != 0 || item.RequiredHonorRank != 0 || item.RequiredCityRank != 0 {
			return "equipped_tool_requirements_unsupported"
		}
		if item.RequiredSkill != 0 {
			own, ok := s.view.skills[item.RequiredSkill]
			if !ok || own.value < item.RequiredSkillRank {
				return "equipped_tool_skill_requirement_not_met"
			}
		}
		for _, object := range worldObjects(s.view.raw, "own_fishing_bobbers") {
			if object != nil && object.Present {
				return "prior_own_fishing_bobber_still_present"
			}
		}
		return ""
	}
	if !recipe.requirementsVerified {
		return recipe.unverifiedReason
	}
	if recipe.focusID != 0 && !s.hasFocusLocked(recipe.focusID) {
		return "required_current_cooking_focus_not_verified"
	}
	return ""
}

func (s *Service) hasFocusLocked(focusID uint32) bool {
	p := s.view.self
	x, y, z := p.X, p.Y, p.Z
	// The world snapshot may retain the last server position after this client
	// walks. Actions explicitly labels its successful movement estimate.
	if s.actions != nil && s.actions.Position != nil && s.actions.Position.Known {
		position := s.actions.Position
		if !position.MapKnown || position.MapId != p.MapId {
			return false
		}
		x, y, z = position.X, position.Y, position.Z
	}
	for _, coordinate := range []float32{x, y, z} {
		if math.IsNaN(float64(coordinate)) || math.IsInf(float64(coordinate), 0) || math.Abs(float64(coordinate)) > 40000 {
			return false
		}
	}
	for _, object := range worldObjects(s.view.raw, "spell_focuses") {
		if object == nil || !object.Present || !object.TypeKnown || object.GameObjectType != 8 || !object.MapKnown || object.MapId != p.MapId || !object.PositionKnown {
			continue
		}
		if !object.StateKnown || object.State > 1 {
			continue
		}
		meta, ok := s.focuses[object.Entry]
		if !ok || meta.focusID != focusID || meta.radius == 0 {
			continue
		}
		dx, dy, dz := float64(object.X-x), float64(object.Y-y), float64(object.Z-z)
		if math.IsNaN(dx+dy+dz) || math.IsInf(dx+dy+dz, 0) {
			continue
		}
		if dx*dx+dy*dy+dz*dz <= float64(meta.radius)*float64(meta.radius) {
			return true
		}
	}
	return false
}

func (s *Service) snapshotLocked() *professionsv1.State {
	s.state.Active = s.active != nil
	s.state.SpellsKnown = s.book.known
	s.state.Closing = s.closed
	s.state.Recipes = nil
	for _, spec := range starterRecipes {
		reason := s.reasonLocked(spec)
		if spec.skill == 333 && reason == "" {
			reason = "explicit_owned_carried_item_selection_required"
		} else if (spec.skill == 393 || spec.skill == 182) && reason == "" {
			reason = "explicit_observed_gathering_target_selection_required"
		}
		s.state.Recipes = append(s.state.Recipes, &professionsv1.Recipe{SkillId: spec.skill, SpellId: spec.spell, Name: spec.name, Known: s.book.has(spec.spell), RequirementsVerified: spec.requirementsVerified, ReagentEntry: spec.reagentEntry, ReagentCount: spec.reagentCount, OutputEntry: spec.itemEntry, OutputCount: spec.itemCount, FocusId: spec.focusID, CastTimeMs: spec.castTimeMs, ChannelDurationMs: spec.channelDurationMs, Ready: reason == "", Reason: reason, MetadataProvenance: metadataProvenance})
	}
	s.state.SkinningTargets = s.skinningTargetsLocked()
	s.state.HerbTargets = s.herbTargetsLocked()
	s.state.DisenchantItem = s.disenchantItemLocked()
	if s.last != nil {
		s.state.Operation = s.operationSnapshotLocked(s.last)
	}
	return proto.Clone(s.state).(*professionsv1.State)
}

func (s *Service) operationSnapshotLocked(op *operation) *professionsv1.Operation {
	out := proto.Clone(op.receipt).(*professionsv1.Operation)
	// A controller result is provisional until its owned cleanup finishes.
	if s.active == op && terminalStatus(out.Status) {
		out.Status = professionsv1.AttemptStatus_WAITING_OBSERVATIONS
		out.Reason = "terminal_receipts_waiting_for_owned_cleanup"
	}
	if age := s.now().Sub(op.started); age > 0 {
		out.AgeMs = uint64(age.Milliseconds())
	}
	return out
}

func (s *Service) GetState(ctx context.Context, _ *emptypb.Empty) (*professionsv1.State, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingDisenchant != nil {
		s.reconcileDisenchantLocked(s.pendingDisenchant)
	}
	return s.snapshotLocked(), nil
}

// InspectHerb observes only the selected actual cache entry and requests its
// public template. It never casts, travels, opens or claims the object.
func (s *Service) InspectHerb(ctx context.Context, r *professionsv1.HerbInspectRequest) (*professionsv1.State, error) {
	if r == nil || r.SessionId != s.state.SessionId || r.TargetGuid>>48 != 0xf110 || r.TargetGuid&0xffffff == 0 || uint32(r.TargetGuid>>24)&0xffffff == 0 {
		return nil, status.Error(codes.InvalidArgument, "bound_session_and_exact_observed_gameobject_guid_required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "session_closing")
	}
	if s.selectedHerbGUID != r.TargetGuid {
		s.selectedHerbGUID = r.TargetGuid
		s.selectedDisenchantGUID = 0
		s.state.Revision++
	}
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call, r.TargetGuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(), nil
}

// InspectDisenchant only queries one explicitly selected actual carried item.
// It neither ranks gear nor chooses what the user should consume.
func (s *Service) InspectDisenchant(ctx context.Context, r *professionsv1.DisenchantInspectRequest) (*professionsv1.State, error) {
	if r == nil || r.SessionId != s.state.SessionId || r.TargetGuid>>48 != 0x4000 || uint32(r.TargetGuid) == 0 {
		return nil, status.Error(codes.InvalidArgument, "bound_session_and_exact_carried_item_guid_required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "session_closing")
	}
	if s.selectedDisenchantGUID != r.TargetGuid {
		s.selectedDisenchantGUID = r.TargetGuid
		s.selectedHerbGUID = 0
		s.state.Revision++
	}
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = s.observe(call, r.TargetGuid)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingDisenchant != nil {
		s.reconcileDisenchantLocked(s.pendingDisenchant)
	}
	return s.snapshotLocked(), nil
}

func (s *Service) Attempt(ctx context.Context, r *professionsv1.AttemptRequest) (*professionsv1.State, error) {
	if r == nil || r.SessionId != s.state.SessionId || !requestPattern.MatchString(r.RequestId) {
		return nil, status.Error(codes.InvalidArgument, "bound session and unique request ID required")
	}
	s.mu.Lock()
	if old := s.requests[r.RequestId]; old != nil {
		if !proto.Equal(old.request, r) {
			s.mu.Unlock()
			return nil, status.Error(codes.AlreadyExists, "request ID already bound to different input")
		}
		out := s.snapshotLocked()
		out.Operation = s.operationSnapshotLocked(old)
		out.Active = s.active == old
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.observe(call, r.TargetGuid); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "actual own observations unavailable")
	}
	s.mu.Lock()
	if old := s.requests[r.RequestId]; old != nil {
		s.mu.Unlock()
		return s.Attempt(ctx, r)
	}
	spec, ok := recipeForSpell(r.SpellId)
	if !ok || spec.skill != r.SkillId {
		s.mu.Unlock()
		return nil, status.Error(codes.InvalidArgument, "only a supported own-known starter recipe is allowed")
	}
	if r.ExpectedRevision != s.state.Revision || r.ExpectedSkillsRevision != s.state.SkillsRevision || r.ExpectedInventoryRevision == 0 || r.ExpectedInventoryRevision != s.state.InventoryRevision || r.ExpectedBackpackRevision != 0 && r.ExpectedBackpackRevision != s.state.BackpackRevision {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, "state, skills or carried inventory changed; no cast reserved")
	}
	var selectedItem *worldv1.BackpackSlot
	if spec.skill == 333 {
		if r.TargetPositionRevision != 0 {
			s.mu.Unlock()
			return nil, status.Error(codes.InvalidArgument, "carried_item_target_has_no_position_revision")
		}
		var reason string
		selectedItem, reason = s.disenchantTargetLocked(r.TargetGuid, nil)
		if reason != "" {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
	} else if spec.skill == 182 {
		if _, reason := s.herbalismTargetLocked(r.TargetGuid, r.TargetPositionRevision, nil); reason != "" {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
	} else if spec.skill == 393 {
		if _, reason := s.skinningTargetLocked(r.TargetGuid, r.TargetPositionRevision, nil); reason != "" {
			s.mu.Unlock()
			return nil, status.Error(codes.FailedPrecondition, reason)
		}
	} else if r.TargetGuid != 0 || r.TargetPositionRevision != 0 {
		s.mu.Unlock()
		return nil, status.Error(codes.InvalidArgument, "target_only_supported_for_fixed_gathering_spells")
	}
	if reason := s.reasonLocked(spec); reason != "" {
		s.mu.Unlock()
		return nil, status.Error(codes.FailedPrecondition, reason)
	}
	if len(s.requests) >= 256 {
		s.mu.Unlock()
		return nil, status.Error(codes.ResourceExhausted, "per-session attempt receipt capacity reached; no cast sent")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.castCount++
	duration := 10 * time.Second
	if spec.skill == 356 {
		duration = 25 * time.Second
	}
	opCtx, stop := context.WithTimeout(context.Background(), duration)
	op := &operation{request: proto.Clone(r).(*professionsv1.AttemptRequest), spec: spec, baseline: s.view, started: s.now(), ctx: opCtx, cancel: stop, done: make(chan struct{}), stored: make(map[byte]bool), removed: make(map[byte]bool), pushed: make(map[byte]bool), pushCounts: make(map[uint32]uint32), receipt: &professionsv1.Operation{OperationId: hex.EncodeToString(id[:]), RequestId: r.RequestId, SkillId: r.SkillId, SpellId: r.SpellId, CastCount: uint32(s.castCount), Status: professionsv1.AttemptStatus_RESERVED, Reason: "reserved_before_cast", BaselineSkillsRevision: s.view.skillsRevision, BaselineBackpackRevision: s.view.backpackRevision, BaselineInventoryRevision: s.view.inventoryRevision, TargetGuid: r.TargetGuid, TargetPositionRevision: r.TargetPositionRevision, BeforeRank: s.view.skills[spec.skill].value, AfterRank: s.view.skills[spec.skill].value, Limitation: limitation}}
	if spec.skill == 182 {
		s.selectedHerbGUID = r.TargetGuid
		s.selectedDisenchantGUID = 0
	}
	if spec.skill == 333 {
		s.selectedDisenchantGUID = r.TargetGuid
		s.selectedHerbGUID = 0
		op.receipt.SourceBag = selectedItem.Bag
		op.receipt.SourceSlot = selectedItem.Slot
		op.receipt.TargetEntry = selectedItem.Entry
	}
	s.requests[r.RequestId] = op
	s.active = op
	s.last = op
	s.state.Revision++
	out := s.snapshotLocked()
	s.mu.Unlock()
	go s.run(op)
	return out, nil
}

func (s *Service) Cancel(_ context.Context, r *professionsv1.CancelRequest) (*professionsv1.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r == nil || r.SessionId != s.state.SessionId || r.OperationId == "" || s.last == nil || s.last.receipt.OperationId != r.OperationId {
		return nil, status.Error(codes.FailedPrecondition, "exact owned operation required")
	}
	if s.active == s.last {
		s.last.receipt.CancelRequested = true
		s.last.cancel()
		s.state.Revision++
	}
	return s.snapshotLocked(), nil
}

func (s *Service) updateEvidenceLocked(view worldView) {
	op := s.active
	if op == nil {
		return
	}
	r := op.receipt
	if view.skillsRevision > r.BaselineSkillsRevision {
		if rank, ok := view.skills[op.spec.skill]; ok && rank.value >= r.BeforeRank {
			r.ObservedSkillsRevision = view.skillsRevision
			r.AfterRank = rank.value
			r.SkillIncreased = rank.value > r.BeforeRank
		}
	}
	if view.inventoryRevision > r.BaselineInventoryRevision {
		r.ObservedInventoryRevision = view.inventoryRevision
		r.ObservedBackpackRevision = view.backpackRevision
		if op.spec.reagentEntry != 0 {
			before := op.baseline.counts[op.spec.reagentEntry]
			r.ReagentsConsumedObserved = before >= op.spec.reagentCount && view.counts[op.spec.reagentEntry] <= before-op.spec.reagentCount
		}
		if op.spec.itemEntry != 0 {
			before := op.baseline.counts[op.spec.itemEntry]
			after := view.counts[op.spec.itemEntry]
			r.OutputInventoryObserved = after >= before && after-before >= op.spec.itemCount
		}
	}
}

func (s *Service) BeforeLogout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.mu.Lock()
	s.closed = true
	s.state.Revision++
	op := s.active
	if op != nil {
		op.receipt.CancelRequested = true
		op.cancel()
	}
	s.mu.Unlock()
	if op != nil {
		select {
		case <-op.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &emptypb.Empty{}, nil
}

func fail(reason string) error { return fmt.Errorf("%s", reason) }
