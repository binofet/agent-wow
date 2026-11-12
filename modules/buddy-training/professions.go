package buddytraining

import (
	"context"
	"sort"

	trainingv1 "github.com/agent-wow/agent-wow/modules/buddy-training/api"
	worldv1 "github.com/agent-wow/agent-wow/modules/buddy-world/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type professionSpec struct {
	skill, wrapper, learned uint32
	name                    string
}

// Build12340 Spell.dbc, indexed by DBCStructure.h: each wrapper has LEARN_SPELL
// effect36 triggering the first-rank skill spell, plus SKILL_STEP44 for this
// exact skill with basepoints0. Trainer.cpp advertises one primary point.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Entities/Creature/Trainer.cpp
// These initial spells are also present in the public trainer_spell table.
var initialProfessions = []professionSpec{
	{129, 3279, 3273, "First Aid"},
	{164, 2020, 2018, "Blacksmithing"},
	{165, 2155, 2108, "Leatherworking"},
	{171, 2275, 2259, "Alchemy"},
	{182, 2372, 2366, "Herbalism"},
	{185, 2551, 2550, "Cooking"},
	{186, 2581, 2575, "Mining"},
	{197, 3911, 3908, "Tailoring"},
	{202, 4039, 4036, "Engineering"},
	{333, 7414, 7411, "Enchanting"},
	{356, 7733, 7620, "Fishing"},
	{393, 8615, 8613, "Skinning"},
	{755, 25245, 25229, "Jewelcrafting"},
	{773, 45375, 45357, "Inscription"},
}

func primarySkill(id uint32) bool {
	// SharedDefines.h SkillType: count all primary professions, including
	// those outside the initial-learning allowlist. Secondary skills don't
	// consume primary slots and cannot authorize a profession purchase.
	switch id {
	case 164, 165, 171, 182, 186, 197, 202, 333, 393, 755, 773:
		return true
	}
	return false
}

func secondarySkill(id uint32) bool { return id == 129 || id == 185 || id == 356 }

func professionForSkill(id uint32) (professionSpec, bool) {
	for _, p := range initialProfessions {
		if p.skill == id {
			return p, true
		}
	}
	return professionSpec{}, false
}

func annotateProfessions(o *trainingv1.Offer) {
	if o.TrainerType != 2 {
		return
	}
	for _, spell := range o.Spells {
		for _, p := range initialProfessions {
			if spell.SpellId == p.wrapper {
				spell.ProfessionSkillId, spell.LearnedSpellId, spell.ProfessionName = p.skill, p.learned, p.name
				spell.PrimaryProfession = primarySkill(p.skill)
			}
		}
	}
}

func professionRequest(r *trainingv1.ProfessionBuyRequest) (professionSpec, error) {
	if r == nil || r.Purchase == nil {
		return professionSpec{}, status.Error(codes.InvalidArgument, "exact profession purchase required")
	}
	p, ok := professionForSkill(r.SkillId)
	if !ok || p.wrapper != r.Purchase.SpellId {
		return professionSpec{}, status.Error(codes.InvalidArgument, "only the verified initial profession trainer spell is supported")
	}
	if secondarySkill(p.skill) {
		if len(r.AllowedPrimarySkills) != 0 && len(r.AllowedPrimarySkills) != 2 {
			return professionSpec{}, status.Error(codes.InvalidArgument, "secondary training accepts no primary assignment or an exact pair")
		}
		if len(r.AllowedPrimarySkills) == 2 && (r.AllowedPrimarySkills[0] == r.AllowedPrimarySkills[1] || !primarySkill(r.AllowedPrimarySkills[0]) || !primarySkill(r.AllowedPrimarySkills[1])) {
			return professionSpec{}, status.Error(codes.InvalidArgument, "secondary training received a malformed primary assignment")
		}
		return p, nil
	}
	if len(r.AllowedPrimarySkills) != 2 || r.AllowedPrimarySkills[0] == r.AllowedPrimarySkills[1] {
		return professionSpec{}, status.Error(codes.InvalidArgument, "two distinct authorized primary professions required")
	}
	allowed := false
	for _, id := range r.AllowedPrimarySkills {
		if _, ok := professionForSkill(id); !ok || !primarySkill(id) {
			return professionSpec{}, status.Error(codes.InvalidArgument, "authorized profession is outside the supported initial catalog")
		}
		allowed = allowed || id == r.SkillId
	}
	if !allowed {
		return professionSpec{}, status.Error(codes.InvalidArgument, "requested profession is not in the explicitly authorized pair")
	}
	return p, nil
}

func (s *Service) DiscoverProfessions(ctx context.Context, _ *emptypb.Empty) (*trainingv1.Discovery, error) {
	return s.discover(ctx, 2)
}

func (s *Service) BuyProfession(ctx context.Context, r *trainingv1.ProfessionBuyRequest) (*trainingv1.State, error) {
	if _, err := professionRequest(r); err != nil {
		return nil, err
	}
	return s.buy(ctx, r.Purchase, r)
}

func observedProfessions(w *worldv1.SnapshotResponse) ([]*trainingv1.ProfessionSkill, bool) {
	if !w.SelfSkillsKnown || len(w.SelfSkills) != 128 {
		return nil, false
	}
	seenSlots, seenSkills := map[uint32]bool{}, map[uint32]bool{}
	var out []*trainingv1.ProfessionSkill
	for _, q := range w.SelfSkills {
		if q == nil || q.Slot >= 128 || seenSlots[q.Slot] || q.SkillId > 65535 || q.Step > 65535 || q.Value > q.MaxValue || q.MaxValue > 65535 {
			return nil, false
		}
		seenSlots[q.Slot] = true
		if q.SkillId == 0 {
			if q.Step != 0 || q.Value != 0 || q.MaxValue != 0 || q.TemporaryBonus != 0 || q.PermanentBonus != 0 {
				return nil, false
			}
			continue
		}
		if seenSkills[q.SkillId] || q.Value == 0 || q.MaxValue == 0 {
			return nil, false
		}
		seenSkills[q.SkillId] = true
		if primarySkill(q.SkillId) {
			out = append(out, &trainingv1.ProfessionSkill{SkillId: q.SkillId, Rank: q.Value, MaxRank: q.MaxValue, Step: q.Step})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkillId < out[j].SkillId })
	return out, true
}

func (s *Service) applyProfessions(w *worldv1.SnapshotResponse) {
	rows, known := observedProfessions(w)
	var secondary []*trainingv1.ProfessionSkill
	if known {
		for _, q := range w.SelfSkills {
			if secondarySkill(q.SkillId) {
				secondary = append(secondary, &trainingv1.ProfessionSkill{SkillId: q.SkillId, Rank: q.Value, MaxRank: q.MaxValue, Step: q.Step})
			}
		}
		sort.Slice(secondary, func(i, j int) bool { return secondary[i].SkillId < secondary[j].SkillId })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.Self == nil || w.Self.Guid != s.selfGUID || w.SelfSkillsRevision < s.state.ProfessionsRevision || w.SelfFreePrimarySlotsRevision < s.freeSlotsRevision {
		s.state.ProfessionsKnown, s.state.FreePrimarySlotsKnown = false, false
		return
	}
	s.professionLevel, s.professionLevelKnown = w.Self.Level, w.Self.LevelKnown
	s.state.ProfessionsKnown, s.state.ProfessionsRevision, s.state.PrimaryProfessions = known, w.SelfSkillsRevision, rows
	s.state.SecondaryProfessions = secondary
	s.state.FreePrimarySlotsKnown, s.state.FreePrimarySlots = w.SelfFreePrimarySlotsKnown, w.SelfFreePrimarySlots
	s.freeSlotsRevision = w.SelfFreePrimarySlotsRevision
	if p := s.state.LastPurchase; p != nil && p.ProfessionSkillId != 0 && p.Status != trainingv1.PurchaseStatus_SERVER_REJECTED && known && w.SelfSkillsRevision > p.BaselineSkillsRevision {
		for _, q := range append(rows, secondary...) {
			if q.SkillId == p.ProfessionSkillId && q.Rank >= 1 && q.MaxRank >= 75 {
				p.ProfessionSkillObserved = true
				p.ConfirmedSkillsRevision = w.SelfSkillsRevision
			}
		}
		s.confirmLocked()
	}
}

func (s *Service) refreshProfessions(ctx context.Context) error {
	w := &worldv1.SnapshotResponse{}
	if err := s.invoke(ctx, "buddy-world", "snapshot", &worldv1.SnapshotRequest{}, w); err != nil {
		return err
	}
	s.applyProfessions(w)
	if w.Self == nil || w.Self.Guid != s.selfGUID {
		return status.Error(codes.FailedPrecondition, "profession evidence belongs to another character")
	}
	return nil
}

func (s *Service) boundProfessionLocked(r *trainingv1.ProfessionBuyRequest) error {
	p, err := professionRequest(r)
	if err != nil {
		return err
	}
	if !s.state.ProfessionsKnown {
		return status.Error(codes.FailedPrecondition, "complete own skill evidence is required")
	}
	if primarySkill(p.skill) && (!s.state.FreePrimarySlotsKnown || s.state.FreePrimarySlots == 0 || s.state.FreePrimarySlots > 2 || len(s.state.PrimaryProfessions) >= 2) {
		return status.Error(codes.FailedPrecondition, "complete own skills and an available primary profession slot are required; no third profession")
	}
	existing := s.state.PrimaryProfessions
	if secondarySkill(p.skill) {
		existing = s.state.SecondaryProfessions
	}
	for _, q := range existing {
		if q.SkillId == p.skill {
			return status.Error(codes.FailedPrecondition, "requested profession is already observed; no relearning")
		}
		if primarySkill(p.skill) && q.SkillId != r.AllowedPrimarySkills[0] && q.SkillId != r.AllowedPrimarySkills[1] {
			return status.Error(codes.FailedPrecondition, "existing primary profession is outside the authorized pair; preserving it")
		}
	}
	if o := s.state.Offer; o != nil {
		for _, spell := range o.Spells {
			if spell.SpellId != p.wrapper {
				continue
			}
			if spell.ProfessionSkillId != p.skill || spell.LearnedSpellId != p.learned || spell.PrimaryProfession != primarySkill(p.skill) || !s.professionLevelKnown || s.professionLevel < spell.RequiredLevel || spell.RequiredSkill != 0 || spell.RequiredSkillRank != 0 || len(spell.RequiredAbilities) != 3 || spell.RequiredAbilities[0] != 0 || spell.RequiredAbilities[1] != 0 || spell.RequiredAbilities[2] != 0 {
				return status.Error(codes.FailedPrecondition, "initial profession mapping, level or prerequisites are unconfirmed")
			}
		}
	}
	return nil
}
