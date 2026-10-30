// Package buddyquestitems performs bounded normal quest-object/item uses.
package buddyquestitems

// Verified build12340 client Spell.dbc, SpellRange.dbc and SpellRadius.dbc;
// public AzerothCore item/gameobject templates and hallows_end.cpp.
// https://github.com/azerothcore/azerothcore-wotlk/blob/master/src/server/scripts/Events/hallows_end.cpp
// No triggered spell is sent directly by this module.
const (
	questID       uint32 = 11360
	barrelEntry   uint32 = 186234
	fireEntry     uint32 = 23537
	bucketEntry   uint32 = 32971
	createSpell   uint32 = 42144
	throwSpell    uint32 = 42340
	requiredCount uint32 = 5
	limitation           = "Normal barrel use and owned Water Bucket ground casts only. Burning aura and first-hit order are unknown; nearby players can catch a bucket. Cast completion, inventory changes and individual own quest credit are separate evidence. No movement, triggered-spell shortcut, item grant or party-shared credit is assumed."
)
