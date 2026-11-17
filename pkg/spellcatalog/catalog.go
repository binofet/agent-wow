// Package spellcatalog describes spells independently of the controller's class
// or spell IDs. Generic mechanics consume these contracts; knowledge alone does
// not authorize a target or claim an effect occurred.
package spellcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

type Effect struct {
	Kind, A, B, Aura, Trigger, PeriodMs, Chain, Radius uint32
	ItemType                                           uint32
	Points, Dice, Misc                                 int32
	PerLevel                                           float64
}
type Reagent struct{ ID, Count uint32 }
type DispelType uint32

const (
	DispelMagic DispelType = iota + 1
	DispelCurse
	DispelDisease
	DispelPoison
	ordinaryDispelMask = 1<<DispelMagic | 1<<DispelCurse | 1<<DispelDisease | 1<<DispelPoison
)

type Spell struct {
	ID, Family, Rank, Level                                        uint32
	Name                                                           string
	DispelType                                                     DispelType
	Attrs                                                          [8]uint32
	Stances, StancesNot, TargetMask, Focus, Facing                 uint32
	Conditions                                                     [8]uint32
	FamilySet                                                      uint32
	FamilyFlags                                                    [3]uint32
	Behind                                                         bool
	Power                                                          int32
	Cost, CostPerLevel, CostPercent, PerSecond                     uint32
	CastMs, CastMinMs                                              uint32
	CastPerLevel                                                   int32
	TimingKnown, RangeKnown                                        bool
	MinHostile, MinFriendly, MaxHostile, MaxFriendly               float64
	CooldownMs, Category, CategoryCooldownMs, GcdMs, DurationIndex uint32
	ItemClass                                                      int32
	SubclassMask, InventoryMask, RuneCost                          uint32
	Reagents                                                       []Reagent
	Totems                                                         []uint32
	Effects                                                        []Effect
}
type Kind string

const (
	Heal         Kind = "heal"
	Interrupt    Kind = "interrupt"
	Damage       Kind = "damage"
	Buff         Kind = "buff"
	Resurrect    Kind = "resurrect"
	Stealth      Kind = "stealth"
	WeaponStrike Kind = "weapon_strike"
	Polymorph    Kind = "polymorph"
	Sap          Kind = "sap"
	CreateItem   Kind = "create_item"
	Cleanse      Kind = "cleanse"
)

func (kind Kind) BreaksOnDamage() bool { return kind == Polymorph || kind == Sap }

type Capability struct {
	Kind    Kind
	Target  string
	Blocked string
}

// InterruptsCasting reports a native interrupt effect, including spells with
// additional effects or equipment requirements. The caller checks cast eligibility.
func (s Spell) InterruptsCasting() bool {
	for _, effect := range s.Effects {
		if effect.Kind == 68 { // SPELL_EFFECT_INTERRUPT_CAST
			return true
		}
	}
	return false
}

// IsHealing reports a direct or periodic health restore. The caster may be an
// enemy, and the spell may heal that caster, an ally, or an area.
func (s Spell) IsHealing() bool {
	for _, effect := range s.Effects {
		switch effect.Kind {
		case 10, 67, 75, 136: // Regular, maximum health, mechanical, or percent heal.
			return true
		case 6, 27, 35, 65, 119, 128, 129, 143: // Apply aura, including area auras.
			if effect.Aura == 8 { // SPELL_AURA_PERIODIC_HEAL
				return true
			}
		}
	}
	return false
}

// ProvidesWaterBreathing recognizes single-unit water breathing auras even
// when their spells have requirements outside the ordinary buff handler.
func (s Spell) ProvidesWaterBreathing() bool {
	for _, e := range s.Effects {
		if e.Kind == 6 && e.Aura == 82 && (e.A == 1 || e.A == 21) && e.B == 0 && e.Chain <= 1 && e.Radius == 0 {
			return true
		}
	}
	return false
}

// FriendlyDispelMask describes direct single-unit dispels. A selector of 25
// can also target an enemy in game; the cleanse handler restricts it to allies.
func (s Spell) FriendlyDispelMask() uint32 {
	if len(s.Effects) == 0 || s.TargetMask != 0 {
		return 0
	}
	var mask uint32
	for _, e := range s.Effects {
		if e.Kind != 38 || (e.A != 21 && e.A != 25) || e.A != s.Effects[0].A ||
			e.B != 0 || e.Chain != 0 || e.Radius != 0 || e.Aura != 0 || e.Trigger != 0 || e.PeriodMs != 0 {
			return 0
		}
		switch DispelType(e.Misc) {
		case DispelMagic, DispelCurse, DispelDisease, DispelPoison:
			mask |= 1 << e.Misc
		case 7: // DISPEL_ALL, limited by the realm to these four types.
			mask |= ordinaryDispelMask
		default:
			return 0
		}
	}
	return mask
}

// CreatedItem is the single output of a direct self-target item creation.
// Capability additionally applies the normal learned-cast requirements.
func (s Spell) CreatedItem() uint32 {
	if len(s.Effects) != 1 || s.TargetMask != 0 {
		return 0
	}
	e := s.Effects[0]
	if e.Kind != 24 || e.A != 1 || e.B != 0 || e.Aura != 0 || e.Trigger != 0 ||
		e.PeriodMs != 0 || e.Chain != 0 || e.Radius != 0 || e.ItemType >= 1<<31 {
		return 0
	}
	return e.ItemType
}

// Capabilities match spell mechanics. Specialized handlers can further restrict their known spell families, ranks and effects.
// Deliberately expose unsupported requirements rather than silently hiding them.
func (s Spell) Capability() Capability {
	fail := func(why string) Capability { return Capability{Blocked: why} }
	if s.Attrs[0]&0x40 != 0 {
		return fail("passive")
	}
	if !s.TimingKnown || !s.RangeKnown {
		return fail("timing_or_range_unknown")
	}
	if s.Attrs[1]&(0x4|0x40) != 0 || s.PerSecond != 0 {
		return fail("channel_requires_handler")
	}
	if s.Focus != 0 || len(s.Totems) > 0 || len(s.Reagents) > 0 || s.RuneCost != 0 {
		return fail("material_or_focus_requirement")
	}
	for i, v := range s.Conditions {
		if v != 0 && i != 0 && i != 2 {
			return fail("aura_state_requirement")
		}
	}
	if s.ItemClass == -1 && s.Power == 0 && s.MaxFriendly > 0 && s.FriendlyDispelMask() != 0 {
		return Capability{Kind: Cleanse, Target: "ally"}
	}
	// The ordinary learned sheep ranks use a single enemy, confuse and transform.
	if s.Family == 118 && (s.ID == 118 || s.ID >= 12824 && s.ID <= 12826) &&
		s.FamilySet == 3 && s.ItemClass == -1 && s.Power == 0 && s.CastMs == 1500 &&
		s.MaxHostile == 30 && s.DurationIndex != 0 && s.TargetMask == 0 &&
		len(s.Effects) == 2 {
		confuse, transform := s.Effects[0], s.Effects[1]
		if confuse.Kind == 6 && transform.Kind == 6 && confuse.Aura == 5 && transform.Aura == 56 &&
			confuse.A == 6 && transform.A == 6 && confuse.B == 0 && transform.B == 0 &&
			confuse.Trigger == 0 && transform.Trigger == 0 && confuse.Chain == 0 && transform.Chain == 0 &&
			confuse.Radius == 0 && transform.Radius == 0 {
			return Capability{Kind: Polymorph, Target: "enemy"}
		}
	}
	// Learned rogue Sap ranks use an explicit single enemy, energy, Stealth
	// stance and one incapacitate aura. Other aura-12 spells need their own contract.
	if s.Family == 6770 && s.FamilySet == 8 && s.Stances == 1<<29 && s.Attrs[0]&0x20000 != 0 &&
		s.ItemClass == 2 && s.SubclassMask == 173555 && s.InventoryMask == 0 &&
		s.Power == 3 && s.CastMs == 0 && s.MaxHostile == 5 && s.DurationIndex != 0 &&
		s.TargetMask == 0 && len(s.Effects) == 1 {
		effect := s.Effects[0]
		if effect.Kind == 6 && effect.A == 6 && effect.B == 0 && effect.Aura == 12 &&
			effect.Trigger == 0 && effect.Chain == 0 && effect.Radius == 0 {
			return Capability{Kind: Sap, Target: "enemy"}
		}
	}
	kind := Kind("")
	target := ""
	for _, e := range s.Effects {
		if e.B != 0 || e.Chain > 1 || e.Radius != 0 {
			return fail("area_or_multi_target_requires_handler")
		}
		t := ""
		switch e.A {
		case 0:
			if s.TargetMask&0x8000 != 0 {
				t = "dead_ally"
			}
		case 1:
			t = "self"
		case 6:
			t = "enemy"
		case 21:
			t = "ally"
		default:
			return fail(fmt.Sprintf("target_selector_%d_requires_handler", e.A))
		}
		k := Kind("")
		switch e.Kind {
		case 10:
			k = Heal
		case 68:
			k = Interrupt
		case 2:
			k = Damage
		case 18, 113:
			k = Resurrect
			t = "dead_ally"
		case 121, 31, 58, 17:
			k = WeaponStrike
		case 24:
			if s.CreatedItem() == 0 {
				return fail("effect_24_requires_handler")
			}
			k = CreateItem
		case 80:
			continue // combo point award accompanies a weapon strike
		case 6:
			if e.Aura == 3 && t == "enemy" {
				k = Damage
			} else if e.Aura == 16 {
				k = Stealth
				t = "self"
			} else if e.Aura == 36 || e.Aura == 33 {
				continue
			} else {
				switch e.Aura {
				case 22, 29, 42, 118:
					k = Buff
				case 58, 82: // Swim speed may accompany a friendly water-breathing buff.
					if !s.ProvidesWaterBreathing() || (t != "self" && t != "ally") {
						return fail(fmt.Sprintf("aura_%d_requires_handler", e.Aura))
					}
					k = Buff
				default:
					return fail(fmt.Sprintf("aura_%d_requires_handler", e.Aura))
				}
			}
		default:
			return fail(fmt.Sprintf("effect_%d_requires_handler", e.Kind))
		}
		if kind != "" && kind != k {
			return fail("mixed_effects_require_handler")
		}
		if target != "" && target != t {
			return fail("mixed_targets_require_handler")
		}
		kind, target = k, t
	}
	if kind == "" || target == "" {
		return fail("no_implemented_effect")
	}
	if (kind == Heal || kind == Buff) && target != "ally" && target != "self" {
		return fail("non_friendly_support_target")
	}
	if kind == Interrupt && target != "enemy" {
		return fail("non_hostile_interrupt_target")
	}
	if (kind == Damage || kind == WeaponStrike) && target != "enemy" {
		return fail("non_hostile_damage_target")
	}
	if s.ItemClass >= 0 && kind != WeaponStrike {
		return fail("equipment_requirement")
	}
	return Capability{Kind: kind, Target: target}
}

type span struct{ start, end int }
type Catalog struct {
	data        []byte
	index       map[uint32]span
	dispelTypes map[uint32]DispelType
}

// New indexes compact line records once. Individual records are decoded on
// lookup, so unknown/NPC spells do not create thousands of runtime objects.
func New(data []byte) (*Catalog, error) {
	c := &Catalog{data: append([]byte(nil), data...), index: map[uint32]span{}}
	for start := 0; start < len(c.data); {
		end := bytes.IndexByte(c.data[start:], '\n')
		if end < 0 {
			end = len(c.data) - start
		}
		end += start
		row := c.data[start:end]
		if len(row) > 0 {
			if !bytes.HasPrefix(row, []byte(`{"id":`)) {
				return nil, fmt.Errorf("missing leading spell ID")
			}
			comma := bytes.IndexByte(row, ',')
			if comma < 6 {
				return nil, fmt.Errorf("invalid spell record")
			}
			id, err := strconv.ParseUint(string(row[6:comma]), 10, 32)
			if err != nil || id == 0 {
				return nil, fmt.Errorf("invalid spell ID")
			}
			if _, exists := c.index[uint32(id)]; exists {
				return nil, fmt.Errorf("duplicate spell ID")
			}
			c.index[uint32(id)] = span{start, end}
		}
		start = end + 1
	}
	return c, nil
}
func (c *Catalog) Lookup(id uint32) (Spell, bool) {
	if c == nil {
		return Spell{}, false
	}
	p, ok := c.index[id]
	if !ok {
		return Spell{}, false
	}
	var s Spell
	if json.Unmarshal(c.data[p.start:p.end], &s) != nil || s.ID != id {
		return Spell{}, false
	}
	if kind := c.dispelTypes[id]; kind != 0 {
		s.DispelType = kind
	}
	return s, true
}

//go:embed spells.jsonl
var records []byte

//go:embed dispel-types.json
var dispelTypes []byte

var once sync.Once
var defaultCatalog *Catalog

func Default() *Catalog {
	once.Do(func() {
		var err error
		defaultCatalog, err = New(records)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(dispelTypes, &defaultCatalog.dispelTypes); err != nil {
			panic(err)
		}
	})
	return defaultCatalog
}
