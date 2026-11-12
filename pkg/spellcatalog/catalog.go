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
	Points, Dice, Misc                                 int32
	PerLevel                                           float64
}
type Reagent struct{ ID, Count uint32 }
type Spell struct {
	ID, Family, Rank, Level                                        uint32
	Name                                                           string
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
	Damage       Kind = "damage"
	Buff         Kind = "buff"
	Resurrect    Kind = "resurrect"
	Stealth      Kind = "stealth"
	WeaponStrike Kind = "weapon_strike"
)

type Capability struct {
	Kind    Kind
	Target  string
	Blocked string
}

// Capabilities match effects/target selectors, never names, classes or spell IDs.
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
		case 2:
			k = Damage
		case 18, 113:
			k = Resurrect
			t = "dead_ally"
		case 121, 31, 58, 17:
			k = WeaponStrike
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
	data  []byte
	index map[uint32]span
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
	return s, true
}

//go:embed spells.jsonl
var records []byte
var once sync.Once
var defaultCatalog *Catalog

func Default() *Catalog {
	once.Do(func() {
		var err error
		defaultCatalog, err = New(records)
		if err != nil {
			panic(err)
		}
	})
	return defaultCatalog
}
