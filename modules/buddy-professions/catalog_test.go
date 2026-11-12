package buddyprofessions

import (
	"strings"
	"testing"
)

func TestVerifiedStarterCraftsRetainActualDBCInputsOutputsAndCastTimes(t *testing.T) {
	for _, want := range []struct {
		spell, skill, material, product, focus, cast uint32
	}{
		{3275, 129, 2589, 1251, 0, 1500},
		{2538, 185, 2672, 2679, 4, 2000},
		{2540, 185, 769, 2681, 4, 2000},
	} {
		got, ok := recipeForSpell(want.spell)
		if !ok || got.skill != want.skill || got.reagentEntry != want.material || got.itemEntry != want.product || got.focusID != want.focus || got.castTimeMs != want.cast || got.reagentCount != 1 || got.itemCount != 1 {
			t.Fatalf("spell%d metadata: %+v", want.spell, got)
		}
		if !got.requirementsVerified || got.itemClass != -1 || got.subclassMask != 0 || got.minimumSkill != 1 || got.stanceNotMask != 0 || !got.channelDurationKnown || got.channelDurationMs != 0 || got.name == "" || got.unverifiedReason != "" {
			t.Fatalf("ordinary craft requirements became unsupported or channeled: %+v", got)
		}
	}
}

func TestFishingNeverSubstitutesGuessedDurationOrARecipeProduct(t *testing.T) {
	got, ok := recipeForSpell(7620)
	if !ok || got.skill != 356 || got.itemClass != 2 || got.subclassMask != 1<<20 || got.castTimeMs != 0 || got.minimumSkill != 1 || got.stanceNotMask != 2 {
		t.Fatalf("fishing equipment metadata: %+v", got)
	}
	if !got.requirementsVerified || !got.channelDurationKnown || got.channelDurationMs != 17000 || got.itemEntry != 0 || got.itemCount != 0 || got.reagentEntry != 0 || got.reagentCount != 0 || got.unverifiedReason != "" {
		t.Fatalf("supported fishing metadata lost its verified duration or invented a recipe product: %+v", got)
	}
}

func TestCatalogDoesNotTreatTrainingWrappersOrOtherRanksAsStarterRecipes(t *testing.T) {
	seen := map[uint32]bool{}
	for _, recipe := range starterRecipes {
		if seen[recipe.spell] {
			t.Fatal("duplicate starter spell", recipe.spell)
		}
		seen[recipe.spell] = true
	}
	if len(seen) != 7 {
		t.Fatal("starter scope expanded without validation", seen)
	}
	for _, id := range []uint32{0, 3273, 3279, 2550, 2551, 7733, 7731, 3276, 585, 0xffffffff} {
		if _, ok := recipeForSpell(id); ok {
			t.Fatal("nonrecipe or unsupported rank entered starter catalog", id)
		}
	}
	for _, hash := range []string{"build12340", "d5cce1a83550dcfa9eb2f0251dbb11fd24c272534b2b1a9b230924a44d817ab3", "919ca9b65cb144a3a9cf0ce10d2a25fcc7cdccf33c752ed376e086ff62f8ccec", "caa2cbec7fa711bba180af10c253ab0d111609ec2bda1c354fef09e54bc279e4", "4154b833d6a26b9b9ce53851d56cb594f0813c0936a72ec89f933cc69abe42c3"} {
		if !strings.Contains(metadataProvenance, hash) {
			t.Fatal("client metadata lost its reproducible provenance", metadataProvenance)
		}
	}
}

func TestSkinningCatalogIsFixedExplicitTargetSpell(t *testing.T) {
	got, ok := recipeForSpell(8613)
	if !ok || got.skill != 393 || got.minimumSkill != 1 || got.castTimeMs != 1500 || got.itemEntry != 0 || got.reagentEntry != 0 || !got.requirementsVerified {
		t.Fatal(got)
	}
}
