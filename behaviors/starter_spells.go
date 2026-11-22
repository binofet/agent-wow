package behaviors

import _ "embed"

//go:embed encounter-spells.lua
var starterEncounterSpells string

//go:embed healing.lua
var starterHealing string

//go:embed starter-spells.lua
var starterSpells string

//go:embed encounter-movement.lua
var starterMovement string

// StarterSpellSource composes the same editable modules as the full session.
var StarterSpellSource = "local encounter_spells = (function()\n" + starterEncounterSpells + "\nend)()\n" +
	"local encounter_healing = (function()\n" + starterHealing + "\nend)()\n" +
	"local encounter_movement = (function()\n" + starterMovement + "\nend)()\n" + starterSpells
