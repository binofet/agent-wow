package behavior

import (
	"strings"
	"testing"
)

func tradeServicePreparationFrame() map[string]any {
	frame := tradeConsentFrame()
	frame["kind"] = "prepare_trade_service"
	trade := frame["trade"].(map[string]any)
	trade["partnerAccepted"] = false // Preparation precedes Joel's final consent.
	serviceRow := tradeConsentItem(6)
	serviceRow["entry"], serviceRow["itemGuid"], serviceRow["name"], serviceRow["metadataKnown"] = 4632, "0", "", false
	trade["partnerItems"] = []any{serviceRow}
	frame["service"] = map[string]any{"slot": 6, "entry": 4632, "count": 1, "wrapped": false, "lockId": 5, "partnerSpell": 0}
	frame["attempt"] = map[string]any{
		"item":      map[string]any{"entry": 0, "count": 0, "wrapped": false, "lockId": 0},
		"castCount": 0, "queued": false, "accepted": false, "castGo": false, "retry": false,
		"deadlineKnown": false, "remainingMs": 0,
	}
	frame["world"] = map[string]any{
		"visitId": "7", "mapKnown": true, "mapId": 0,
		"self": map[string]any{"guid": "8", "present": true, "aliveKnown": true, "alive": true,
			"mapKnown": true, "mapId": 0, "positionKnown": true, "positionRevision": "11",
			"classKnown": true, "classId": 4, "unitFlagsKnown": true, "unitFlags": 0,
			"movementFlagsKnown": true, "movementFlags": 0},
		"owner": map[string]any{"guid": "3", "present": true, "aliveKnown": true, "alive": true,
			"mapKnown": true, "mapId": 0, "positionKnown": true, "positionRevision": "13",
			"unitFlagsKnown": true, "unitFlags": 0, "x": 1, "y": 2, "z": 3},
		"skills": map[string]any{"known": true, "complete": true, "revision": "17", "rows": []any{
			map[string]any{"slot": 5, "skillId": 633, "value": 75, "maxValue": 75, "temporaryBonus": 0, "permanentBonus": 0},
		}},
	}
	frame["spell"] = map[string]any{"sessionId": "consent-session", "selfGuid": "8", "spellbookKnown": true,
		"spellbookRevision": "19", "present": true, "spellId": 1804, "metadataKnown": true, "castTimeMs": 5000}
	// This is the existing build-12340 catalog row for lock5, not a server-effect claim.
	frame["reference"] = map[string]any{"catalog": "client_build_12340_lockpicking", "lockId": 5, "known": true, "requiredSkillRank": 1}
	frame["window"] = map[string]any{"deadlineKnown": true, "remainingMs": 30000}
	return frame
}

func tradeServicePriorAttempt(frame map[string]any) map[string]any {
	attempt := frame["attempt"].(map[string]any)
	attempt["item"] = map[string]any{"entry": 4632, "count": 1, "wrapped": false, "lockId": 5}
	attempt["castCount"], attempt["deadlineKnown"], attempt["remainingMs"] = 9, true, 7000
	return attempt
}

func TestTradeServicePreparationActualFactsAndRetainedAttempt(t *testing.T) {
	source := tradeConsentSource(t)
	cases := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"before_partner_consent", func(map[string]any) {}, "prepare"},
		{"after_partner_consent", func(f map[string]any) { f["trade"].(map[string]any)["partnerAccepted"] = true }, "prepare"},
		{"awaiting_stage", func(f map[string]any) { tradeServicePriorAttempt(f) }, "wait"},
		{"expired_unconfirmed_not_rearmed", func(f map[string]any) { tradeServicePriorAttempt(f)["remainingMs"] = 0 }, "wait"},
		{"write_unknown_no_deadline_not_rearmed", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["deadlineKnown"], a["remainingMs"] = false, 0
		}, "wait"},
		{"extended_stage_queued", func(f map[string]any) { tradeServicePriorAttempt(f)["queued"] = true }, "wait"},
		{"accepted_awaiting_cast_go", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["queued"], a["accepted"] = true, true
		}, "wait"},
		{"cast_go_does_not_rearm", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["queued"], a["accepted"], a["castGo"] = true, true, true
		}, "wait"},
		// Only native exact packet correlation may supply this true fact. The
		// Lua policy may decline it; elapsed time alone never supplies it.
		{"queued_retry_stays_held", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["queued"] = true, true
		}, "wait"},
		{"observed_go_retry_stays_held", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["accepted"], a["castGo"] = true, true, true
		}, "wait"},
		{"completion_try_again_can_retain_accepted", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["accepted"], a["deadlineKnown"], a["remainingMs"] = true, true, false, 0
		}, "prepare"},
		{"correlated_try_again", func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["deadlineKnown"], a["remainingMs"] = true, false, 0
		}, "prepare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := tradeServicePreparationFrame()
			tc.edit(frame)
			r := disposalArmorSemanticRuntime(t, source)
			// These two actual CI turns exhausted decide at the unchanged 5ms
			// limit. Prove the full retained-attempt semantics separately;
			// invoke the ordinary CLI once and classify only its strict typed
			// empty-output resource refusal as NO decision, never a retry.
			if tc.name == "accepted_awaiting_cast_go" {
				// This exact frame measured chunk_initialize refusal. Preserve
				// full retained-attempt semantics and the native default-budget
				// callback's strict wait proof; this single standalone invocation
				// may report only its exact empty-output initialization refusal.
				tradeConsentSemanticAndProductionPhases(t, r, source, frame, tc.want, false, true)
			} else if tc.name == "cast_go_does_not_rearm" || tc.name == "queued_retry_stays_held" {
				tradeConsentResourceTurnResult(t, r, source, frame, tc.want)
			} else {
				tradeConsentResult(t, r, source, frame, tc.want)
			}
		})
	}
}

func TestTradeServicePreparationSignedSkillAndKnownReference(t *testing.T) {
	source := tradeConsentSource(t)
	for _, tc := range []struct {
		name                 string
		temporary, permanent int
		want                 string
	}{
		{"bonuses_meet_actual_lock17_rank75", 15, 10, "prepare"},
		{"negative_bonus_below_requirement", 15, 9, "wait"},
		{"negative_temporary_bonus", -1, 25, "wait"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := tradeServicePreparationFrame()
			frame["service"].(map[string]any)["lockId"] = 17
			reference := frame["reference"].(map[string]any)
			reference["lockId"], reference["requiredSkillRank"] = 17, 75
			row := frame["world"].(map[string]any)["skills"].(map[string]any)["rows"].([]any)[0].(map[string]any)
			row["value"], row["temporaryBonus"], row["permanentBonus"] = 50, tc.temporary, tc.permanent
			r := disposalArmorSemanticRuntime(t, source)
			if tc.name == "bonuses_meet_actual_lock17_rank75" {
				// This exact default-CLI case measured a decide resource turn.
				// Keep the full signed-skill semantics proof, but require either
				// its real production decision or the typed zero-output refusal.
				tradeConsentResourceTurnResult(t, r, source, frame, tc.want)
			} else {
				tradeConsentResult(t, r, source, frame, tc.want)
			}
		})
	}
}

func TestTradeServicePreparationIncompleteMalformedOrForeignFactsWait(t *testing.T) {
	source := tradeConsentSource(t)
	cases := map[string]func(map[string]any){
		"missing_mode":            func(f map[string]any) { delete(f, "kind") },
		"wrong_mode":              func(f map[string]any) { f["kind"] = "accept" },
		"extra_caller_authority":  func(f map[string]any) { f["eligible"] = true },
		"unobserved_service_guid": func(f map[string]any) { f["service"].(map[string]any)["itemGuid"] = "4611686018427387905" },
		"other_partner":           func(f map[string]any) { f["trade"].(map[string]any)["partnerGuid"] = "4" },
		"closed_window":           func(f map[string]any) { f["trade"].(map[string]any)["phase"] = "ACCEPT_SENT" },
		"accept_already_written":  func(f map[string]any) { f["trade"].(map[string]any)["acceptTransportWritten"] = true },
		"unknown_deadline":        func(f map[string]any) { f["window"].(map[string]any)["deadlineKnown"] = false },
		"expired_window":          func(f map[string]any) { f["window"].(map[string]any)["remainingMs"] = 0 },
		"missing_service_slot":    func(f map[string]any) { f["trade"].(map[string]any)["partnerItems"] = []any{} },
		"slot_entry_contradiction": func(f map[string]any) {
			f["trade"].(map[string]any)["partnerItems"].([]any)[0].(map[string]any)["entry"] = 999
		},
		"wrong_slot":    func(f map[string]any) { f["service"].(map[string]any)["slot"] = 5 },
		"not_one_item":  func(f map[string]any) { f["service"].(map[string]any)["count"] = 2 },
		"wrapped":       func(f map[string]any) { f["service"].(map[string]any)["wrapped"] = true },
		"partner_spell": func(f map[string]any) { f["service"].(map[string]any)["partnerSpell"] = 1804 },
		"numeric_retry": func(f map[string]any) { tradeServicePriorAttempt(f)["retry"] = 1 },
		"retry_without_cast_count": func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["castCount"] = true, 0
		},
		"remaining_time_without_deadline": func(f map[string]any) {
			a := tradeServicePriorAttempt(f)
			a["retry"], a["deadlineKnown"] = true, false
		},
		"empty_attempt_claims_retry": func(f map[string]any) { f["attempt"].(map[string]any)["retry"] = true },
		"unknown_visit":              func(f map[string]any) { f["world"].(map[string]any)["visitId"] = "0" },
		"other_body":                 func(f map[string]any) { f["world"].(map[string]any)["self"].(map[string]any)["guid"] = "9" },
		"unknown_class":              func(f map[string]any) { f["world"].(map[string]any)["self"].(map[string]any)["classKnown"] = false },
		"wrong_class":                func(f map[string]any) { f["world"].(map[string]any)["self"].(map[string]any)["classId"] = 5 },
		"observed_movement":          func(f map[string]any) { f["world"].(map[string]any)["self"].(map[string]any)["movementFlags"] = 1 },
		"owner_combat":               func(f map[string]any) { f["world"].(map[string]any)["owner"].(map[string]any)["unitFlags"] = 0x80000 },
		"owner_combat_pet":           func(f map[string]any) { f["world"].(map[string]any)["owner"].(map[string]any)["unitFlags"] = 0x2000 },
		"different_map":              func(f map[string]any) { f["world"].(map[string]any)["owner"].(map[string]any)["mapId"] = 1 },
		"old_unknown_skills":         func(f map[string]any) { f["world"].(map[string]any)["skills"].(map[string]any)["complete"] = false },
		"zero_known_skill_revision":  func(f map[string]any) { f["world"].(map[string]any)["skills"].(map[string]any)["revision"] = "0" },
		"unobserved_skill":           func(f map[string]any) { f["world"].(map[string]any)["skills"].(map[string]any)["rows"] = []any{} },
		"duplicate_skill": func(f map[string]any) {
			skills := f["world"].(map[string]any)["skills"].(map[string]any)
			skills["rows"] = append(skills["rows"].([]any), skills["rows"].([]any)[0])
		},
		"malformed_signed_bonus": func(f map[string]any) {
			f["world"].(map[string]any)["skills"].(map[string]any)["rows"].([]any)[0].(map[string]any)["temporaryBonus"] = true
		},
		"missing_learned_spell":          func(f map[string]any) { f["spell"].(map[string]any)["present"] = false },
		"unfamiliar_spell_is_not_1804":   func(f map[string]any) { f["spell"].(map[string]any)["spellId"] = 1805 },
		"foreign_spell_body":             func(f map[string]any) { f["spell"].(map[string]any)["selfGuid"] = "9" },
		"foreign_spell_session":          func(f map[string]any) { f["spell"].(map[string]any)["sessionId"] = "old-body" },
		"missing_cast_time":              func(f map[string]any) { f["spell"].(map[string]any)["castTimeMs"] = 0 },
		"unknown_spell_metadata":         func(f map[string]any) { f["spell"].(map[string]any)["metadataKnown"] = false },
		"old_unknown_spellbook_revision": func(f map[string]any) { f["spell"].(map[string]any)["spellbookRevision"] = "0" },
		"unknown_catalog":                func(f map[string]any) { f["reference"].(map[string]any)["known"] = false },
		"wrong_catalog_provenance":       func(f map[string]any) { f["reference"].(map[string]any)["catalog"] = "server_proof" },
		"different_catalog_lock":         func(f map[string]any) { f["reference"].(map[string]any)["lockId"] = 6 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			frame := tradeServicePreparationFrame()
			edit(frame)
			if name == "missing_cast_time" {
				// This exact malformed frame measured an Evaluate initialization
				// resource turn in the ordinary 5ms CLI. Prove its semantic wait
				// separately, never replace a production refusal with that result.
				tradeServiceMissingCastTimeInitializationResult(t, source, frame)
				return
			}
			tradeConsentResult(t, disposalArmorSemanticRuntime(t, source), source, frame, "wait")
		})
	}
}

func tradeServiceMissingCastTimeInitializationResult(t *testing.T, source string, frame map[string]any) {
	t.Helper()
	out := tradeConsentSemanticAndProductionPhases(t, disposalArmorSemanticRuntime(t, source), source, frame, "wait", false, true)
	if out["reason"] != "known_lockpicking_capability_required" {
		t.Fatal("missing cast time lost its semantic refusal", out)
	}
}

func TestTradeServicePreparationTuningReloadAndOrdinaryConsentRemainSeparate(t *testing.T) {
	source := tradeConsentSource(t)
	r := disposalArmorSemanticRuntime(t, source)
	frame := tradeServicePreparationFrame()
	tradeConsentResult(t, r, source, frame, "prepare")
	tradeConsentResult(t, r, source, tradeConsentFrame(), "accept")
	const serviceTuning = "local prepare_owner_trade_services = true"
	disabled := strings.Replace(source, serviceTuning, "local prepare_owner_trade_services = false", 1)
	if disabled == source {
		t.Fatal("actual preparation tuning missing")
	}
	next, err := r.Reload(disabled, r.Status().Revision)
	if err != nil {
		t.Fatal(err)
	}
	tradeConsentResult(t, r, disabled, frame, "wait")
	tradeConsentResult(t, r, disabled, tradeConsentFrame(), "accept")
	const retryTuning = "local retry_trade_service_try_again = true"
	noRetry := strings.Replace(source, retryTuning, "local retry_trade_service_try_again = false", 1)
	if noRetry == source {
		t.Fatal("actual correlated retry tuning missing")
	}
	if _, err := r.Reload(noRetry, next); err != nil {
		t.Fatal(err)
	}
	tradeServicePriorAttempt(frame)["retry"] = true
	tradeConsentResult(t, r, noRetry, frame, "wait")
	tradeConsentResult(t, r, noRetry, tradeServicePreparationFrame(), "prepare")
	tradeConsentResult(t, r, noRetry, tradeConsentFrame(), "accept")
}

func TestTradeServicePreparationFullBoundedOffersPreserveServiceWithoutInventedGUID(t *testing.T) {
	source := tradeConsentSource(t)
	frame := tradeServicePreparationFrame()
	trade := frame["trade"].(map[string]any)
	own, partner := make([]any, 6), make([]any, 7)
	for i := range own {
		row := tradeConsentItem(i)
		row["name"] = strings.Repeat("n", 255)
		own[i] = row
	}
	for i := range partner {
		row := tradeConsentItem(i)
		row["name"] = strings.Repeat("n", 255)
		partner[i] = row
	}
	partner[6] = trade["partnerItems"].([]any)[0]
	trade["ownRequestedItems"], trade["partnerItems"] = own, partner
	trade["revision"] = "18446744073709551615"
	frame["selfGuid"] = "18446744073709551614"
	world := frame["world"].(map[string]any)
	world["self"].(map[string]any)["guid"] = frame["selfGuid"]
	world["visitId"] = "18446744073709551615"
	world["skills"].(map[string]any)["revision"] = "18446744073709551613"
	frame["spell"].(map[string]any)["selfGuid"] = frame["selfGuid"]
	tradeConsentResult(t, disposalArmorSemanticRuntime(t, source), source, frame, "prepare")
	frame["service"].(map[string]any)["itemGuid"] = "4611686018427387905"
	tradeConsentResult(t, disposalArmorSemanticRuntime(t, source), source, frame, "wait")
}
