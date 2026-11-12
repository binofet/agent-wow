package buddychat

import (
	"errors"
	"sort"
	"strings"

	chatv1 "github.com/agent-wow/agent-wow/modules/buddy-chat/api"
)

const ownerGearPrefix = "WBGEAR1\t"

// Actual learned passive spell IDs are read by the normal UI. These skill IDs
// are proficiency facts only, never assertions about profession skill ranks.
var ownerProficiencies = map[uint32]uint32{
	43: 201, 44: 196, 45: 264, 46: 266, 54: 198, 55: 202,
	136: 227, 160: 199, 172: 197, 173: 1180, 176: 2567,
	226: 5011, 228: 5009, 229: 200, 293: 750, 413: 8737,
	414: 9077, 415: 9078, 433: 9116, 473: 15590,
}

func decodeOwnerGearSnapshot(a *ownerFeedAssembly) (*chatv1.OwnerFeedResponse, error) {
	bad := errors.New("invalid complete owner equipment addon snapshot")
	g := &chatv1.OwnerGear{Available: true, AddonSession: a.nonce, Revision: a.revision, EquipmentRevision: a.revision}
	seen, profs := map[uint32]bool{}, map[uint32]bool{}
	header := false
	for _, part := range a.tokens[:a.parts] {
		for _, token := range strings.Split(part, ";") {
			if len(token) < 3 || token[1] != '=' {
				return nil, bad
			}
			fields := strings.Split(token[2:], ",")
			nums := make([]uint32, len(fields))
			for i, f := range fields {
				n, err := ownerFeedNumber(f, 0, 0x7fffffff)
				if err != nil {
					return nil, bad
				}
				nums[i] = n
			}
			switch token[0] {
			case 'G':
				if header || len(nums) != 5 || nums[2] < 1 || nums[2] > 80 || nums[3] > 1 || nums[4] > 1 {
					return nil, bad
				}
				class, race := nums[0], nums[1]
				if class == 0 || class > 11 || class == 10 || race == 0 || race > 11 || race == 9 {
					return nil, bad
				}
				header = true
				g.ClassKnown, g.ClassId, g.RaceKnown, g.RaceId, g.LevelKnown, g.Level = true, class, true, race, true, nums[2]
				g.GearKnown, g.ProficienciesKnown = nums[3] == 1, nums[4] == 1
			case 'E':
				if len(nums) != 9 || nums[0] > 18 || seen[nums[0]] || nums[1] > 1 || nums[3] > 1 || nums[4] > 1 || nums[5] > 1 || nums[6] > 1 || nums[7] > nums[8] || nums[8] > 1000000 {
					return nil, bad
				}
				if nums[1] == 0 && (nums[2] != 0 || nums[3] != 0 || nums[4] != 0 || nums[5] != 0 || nums[6] != 0 || nums[7] != 0 || nums[8] != 0) {
					return nil, bad
				}
				if nums[4] == 0 && nums[5] != 0 || nums[6] == 0 && (nums[7] != 0 || nums[8] != 0) {
					return nil, bad
				}
				if nums[1] == 1 && nums[2] == 0 && (nums[3] != 1 || nums[4] != 1 || nums[5] != 0 || nums[6] != 1 || nums[7] != 0 || nums[8] != 0) {
					return nil, bad
				}
				seen[nums[0]] = true
				g.Slots = append(g.Slots, &chatv1.OwnerEquipmentSlot{Slot: nums[0], SlotKnown: nums[1] == 1, Entry: nums[2], MetadataKnown: nums[3] == 1, EnhancementsKnown: nums[4] == 1, HasEnhancements: nums[5] == 1, DurabilityKnown: nums[6] == 1, Durability: nums[7], MaxDurability: nums[8]})
			case 'P':
				if len(nums) != 2 || ownerProficiencies[nums[0]] == 0 || nums[1] > 1 || profs[nums[0]] {
					return nil, bad
				}
				profs[nums[0]] = true
				g.Proficiencies = append(g.Proficiencies, &chatv1.OwnerProficiency{SkillId: nums[0], Known: nums[1] == 1})
			default:
				return nil, bad
			}
		}
	}
	if !header || len(seen) != 19 || a.active != 19 || g.ProficienciesKnown && len(profs) != len(ownerProficiencies) || !g.ProficienciesKnown && len(profs) != 0 {
		return nil, bad
	}
	if g.GearKnown {
		for _, s := range g.Slots {
			if !s.SlotKnown || !s.MetadataKnown || !s.EnhancementsKnown || !s.DurabilityKnown {
				return nil, bad
			}
		}
	}
	sort.Slice(g.Slots, func(i, j int) bool { return g.Slots[i].Slot < g.Slots[j].Slot })
	sort.Slice(g.Proficiencies, func(i, j int) bool { return g.Proficiencies[i].SkillId < g.Proficiencies[j].SkillId })
	return &chatv1.OwnerFeedResponse{Available: true, AddonSession: a.nonce, Revision: a.revision, OwnerGear: g}, nil
}

func (s *Service) ingestOwnerGearLocked(payload []byte) bool {
	part, handled, err := decodeOwnerSnapshotPart(payload, ownerGearPrefix, 19)
	if !handled {
		return false
	}
	f := &s.ownerGear
	// A new incomplete/invalid authenticated report invalidates destructive
	// comparisons. Preserve the anti-replay baseline, but never present it as
	// settled equipment while a replacement is being assembled.
	old := f.current
	if err != nil {
		s.ownerGearUnsettled = true
	} else {
		stale := f.current != nil && part.nonce == f.current.AddonSession && part.revision <= f.current.Revision
		for _, nonce := range f.retired {
			if nonce == part.nonce {
				stale = true
			}
		}
		if f.pending != nil && f.pending.nonce == part.nonce && part.revision < f.pending.revision {
			stale = true
		}
		if !stale {
			s.ownerGearUnsettled = true
		}
	}
	s.ingestOwnerSnapshotLocked(part, err, f, decodeOwnerGearSnapshot)
	if f.current != old && f.current != nil {
		s.ownerGearUnsettled = false
	}
	return true
}
