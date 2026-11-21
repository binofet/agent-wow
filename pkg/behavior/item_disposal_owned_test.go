package behavior

import (
	"reflect"
	"strings"
	"testing"
)

func ownedDisposalResult(t *testing.T, r *Runtime, frame map[string]any) map[string]any {
	t.Helper()
	before := r.Status()
	out, err := r.Evaluate(frame)
	after := r.Status()
	reason, ok := out["reason"].(string)
	if err != nil || after.Failures != before.Failures || len(out) != 2 || !ok || reason == "" || len(reason) > 128 {
		t.Fatal("owned classifier failed or returned action authority", out, after, err)
	}
	if out["status"] != "eligible" && out["status"] != "keep" && out["status"] != "unknown" {
		t.Fatal("invalid owned classification", out)
	}
	return out
}

func TestOwnedDisposalMiscellaneousUsesExistingModeWithoutGearOrContext(t *testing.T) {
	r := New()
	load(t, r, disposalPolicySource(t))
	for _, price := range []int{0, 5} {
		item := disposalTemplate(t, false)
		item["sellPrice"] = price
		frame := map[string]any{"mode": "misc_destroy", "item": item}
		existing := ownedDisposalResult(t, r, frame)
		frame["mode"] = "owned_destroy"
		// These authored input keys cannot select an action or supply a shared
		// Lua function. Eligible miscellaneous templates need neither one.
		frame["capability"] = "destroy_anything"
		frame["action"] = map[string]any{"capability": "sell", "source": "unobserved"}
		frame["gear"] = map[string]any{"compare": "caller data is not executable"}
		owned := ownedDisposalResult(t, r, frame)
		if existing["status"] != "eligible" || !reflect.DeepEqual(owned, existing) {
			t.Fatal("owned union changed miscellaneous policy or required armor facts", price, existing, owned)
		}
		frame["mode"] = "vendor_sale"
		vendor := ownedDisposalResult(t, r, frame)
		if price == 0 && vendor["status"] != "keep" || price > 0 && vendor["status"] != "eligible" {
			t.Fatal("owned mode changed existing vendor sale policy", price, vendor)
		}
	}
}

func TestOwnedDisposalArmorDelegatesToActualSharedGearAndReloads(t *testing.T) {
	source, frame := disposalArmorHostProjection(t, disposalArmorFrame(t))
	r := New()
	first := load(t, r, source)
	gray := ownedDisposalResult(t, r, frame)
	frame["mode"] = "owned_destroy"
	before := disposalArmorClone(t, frame)
	owned := ownedDisposalResult(t, r, frame)
	if gray["status"] != "eligible" || !reflect.DeepEqual(owned, gray) || !reflect.DeepEqual(frame, before) {
		t.Fatal("owned union did not reuse exact armor policy", gray, owned)
	}
	const score = "local gain=(candidate.armor or 0)-(current.armor or 0)"
	if strings.Count(source, score) != 1 {
		t.Fatal("actual shared armor scorer not found exactly once")
	}
	second, err := r.Reload(strings.Replace(source, score, score+"-100", 1), first)
	if err != nil || first == second {
		t.Fatal("actual shared scorer edit was not accepted", second, err)
	}
	owned = ownedDisposalResult(t, r, frame)
	frame["mode"] = "gray_armor_destroy"
	gray = ownedDisposalResult(t, r, frame)
	if owned["status"] != "keep" || !reflect.DeepEqual(owned, gray) {
		t.Fatal("owned union bypassed edited shared comparison", owned, gray)
	}
}

func TestOwnedDisposalUnknownBranchesStayFailClosed(t *testing.T) {
	r := New()
	load(t, r, disposalPolicySource(t))
	for name, mutate := range map[string]func(map[string]any){
		"old_template":      func(item map[string]any) { delete(item, "rawTemplateFacts") },
		"unavailable":       func(item map[string]any) { item["available"] = false },
		"pending":           func(item map[string]any) { item["pending"] = true },
		"malformed_quality": func(item map[string]any) { item["quality"] = false },
		"unknown_raw":       func(item map[string]any) { item["rawTemplateFacts"] = nil },
		"functional_item":   func(item map[string]any) { item["hasSpell"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			item := disposalTemplate(t, false)
			mutate(item)
			frame := map[string]any{"mode": "owned_destroy", "item": item, "eligible": true,
				"action": map[string]any{"capability": "destroy_junk"}}
			owned := ownedDisposalResult(t, r, frame)
			if owned["status"] != "unknown" {
				t.Fatal("unproven branch became disposal authority", owned)
			}
		})
	}
}

func TestOwnedDisposalArmorUncertaintyAndFunctionalItemsMatchExistingModes(t *testing.T) {
	_, base := disposalArmorHostProjection(t, disposalArmorFrame(t))
	// These inputs must refuse before scoring. Keep the current classifier
	// and make an unexpected comparison observable, without initializing the
	// unused shared scorer inside each negative case's five-millisecond VM.
	source := "local compared=false\nlocal gear={compare=function(...)compared=true error('refusal reached shared scorer')end}\nlocal disposal=(function()\n" + disposalPolicySource(t) + `
end)()
return {decide=function(input)
 local out=disposal.classify(input.mode,input.item,input.context,gear)
 if compared then error('refusal called shared scorer') end
 return out
end}`
	for name, test := range map[string]struct {
		change func(map[string]any)
		want   string
	}{
		"missing_context":   {func(frame map[string]any) { delete(frame, "context") }, "unknown"},
		"missing_raw_facts": {func(frame map[string]any) { delete(frame["item"].(map[string]any), "rawTemplateFacts") }, "unknown"},
		"unknown_armor":     {func(frame map[string]any) { frame["item"].(map[string]any)["armorComparisonKnown"] = false }, "unknown"},
		"non_gray":          {func(frame map[string]any) { frame["item"].(map[string]any)["quality"] = 1 }, "keep"},
		"functional_flags":  {func(frame map[string]any) { frame["item"].(map[string]any)["flags"] = 1 }, "keep"},
		"functional_spell":  {func(frame map[string]any) { frame["item"].(map[string]any)["hasSpell"] = true }, "keep"},
	} {
		t.Run(name, func(t *testing.T) {
			r := New()
			load(t, r, source)
			frame := disposalArmorClone(t, base)
			test.change(frame)
			gray := ownedDisposalResult(t, r, frame)
			frame["mode"] = "misc_destroy"
			miscellaneous := ownedDisposalResult(t, r, frame)
			frame["mode"] = "owned_destroy"
			owned := ownedDisposalResult(t, r, frame)
			if owned["status"] != test.want || gray["status"] != test.want || miscellaneous["status"] == "eligible" {
				t.Fatal("owned union changed existing refusal semantics", owned, gray, miscellaneous)
			}
		})
	}
}
