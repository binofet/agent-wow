package buddyprofessions

// Static metadata is not evidence that the session character knows a recipe,
// owns its inputs, is equipped correctly, or can use a nearby spell focus.
// Field indices: https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/shared/DataStores/DBCStructure.h
// Spell effects: https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/game/Spells/SpellEffects.cpp
// Extracted from the public build12340 client tables, with little-endian WDBC
// records (Spell:234 words). No server database or configuration was consulted.
// Duration/skill tables were read in memory from the same client's
// Data/enUS/patch-enUS-3.MPQ; no client archive was modified.
const metadataProvenance = "build12340 Spell.dbc sha256:d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3; SpellCastTimes.dbc sha256:919ca9b65cb144a3a9cf0ce10d2a25fcc7cdccf33c752ed376e086ff62f8ccec; SpellDuration.dbc sha256:caa2cbec7fa711bba180af10c253ab0d111609ec2bda1c354fef09e54bc279e4; SkillLineAbility.dbc sha256:4154b833d6a26b9b9ce53851d56cb594f0813c0936a72ec89f933cc69abe42c3"

type recipeSpec struct {
	minimumSkill, stanceNotMask                                             uint32
	skill, spell, reagentEntry, reagentCount, itemEntry, itemCount, focusID uint32
	itemClass                                                               int32
	subclassMask, castTimeMs, channelDurationMs                             uint32
	name                                                                    string
	requirementsVerified                                                    bool
	channelDurationKnown                                                    bool
	unverifiedReason                                                        string
}

// For all three CREATE_ITEM24 recipes, both totems (50..51), all remaining
// reagents/counts (53..59,61..67), inventory mask70, totem categories222..223,
// aura/stance/target restrictions12..27 except cooking focus18, area224,
// rune226 and power costs41..45/204 are zero. ItemClass68 is signed -1 (none).
// Their single effect has basepoints0/diesides1, implicit caster1 and the exact
// created item107; there are no extra effects. CastTimeIndex28 is16 (1500ms)
// for bandages and5 (2000ms) for cooking. No cast duration is a skill-up promise.
// SkillLineAbility has min-rank1 and zero race/class/exclusion masks for all
// four entries. Its acquisition linkage never replaces actual spell knowledge.
var starterRecipes = []recipeSpec{
	{skill: 129, spell: 3275, reagentEntry: 2589, reagentCount: 1, itemEntry: 1251, itemCount: 1, itemClass: -1, minimumSkill: 1, castTimeMs: 1500, name: "Linen Bandage", requirementsVerified: true, channelDurationKnown: true},
	{skill: 185, spell: 2538, reagentEntry: 2672, reagentCount: 1, itemEntry: 2679, itemCount: 1, focusID: 4, itemClass: -1, minimumSkill: 1, castTimeMs: 2000, name: "Charred Wolf Meat", requirementsVerified: true, channelDurationKnown: true},
	{skill: 185, spell: 2540, reagentEntry: 769, reagentCount: 1, itemEntry: 2681, itemCount: 1, focusID: 4, itemClass: -1, minimumSkill: 1, castTimeMs: 2000, name: "Roasted Boar Meat", requirementsVerified: true, channelDurationKnown: true},
	// Fishing is not a deterministic CREATE_ITEM recipe. Its actual row has
	// class2/subclassmask1<<20, no inventory mask/reagents/totems/categories,
	// cast index1 (0ms), duration index575, excluded stance mask2 at14, and
	// effects TRANS_DOOR50 (bobber35591), SKILL118 (356), APPLY_AURA6 (dummy4).
	// Duration575 is exactly17000ms in the actual client table. Bobber expiry
	// is randomized separately by EffectTransmitted; never reel on a timer.
	// Dispatch also requires the actual own form, idle channel and equipped pole.
	{skill: 356, spell: 7620, itemClass: 2, subclassMask: 1 << 20, minimumSkill: 1, stanceNotMask: 2, channelDurationMs: 17000, channelDurationKnown: true, name: "Fishing", requirementsVerified: true},
	{skill: 393, spell: 8613, itemClass: -1, minimumSkill: 1, castTimeMs: 1500, name: "Skinning", requirementsVerified: true, channelDurationKnown: true},
	{skill: 182, spell: 2366, itemClass: -1, minimumSkill: 1, castTimeMs: 3000, name: "Herbalism", requirementsVerified: true, channelDurationKnown: true},
}

func recipeForSpell(spell uint32) (recipeSpec, bool) {
	for _, recipe := range starterRecipes {
		if recipe.spell == spell {
			return recipe, true
		}
	}
	return recipeSpec{}, false
}
