package cards

import (
	"strings"
	"testing"
)

func TestForFamilyReturnsOnlyFamilyCards(t *testing.T) {
	d := NewSeedDeck()
	fam := d.For(TierFamily)

	if len(fam.Prompts) < minFamilyPrompts || len(fam.Answers) < minFamilyAnswers {
		t.Fatalf("family deck too small: %d prompts, %d answers", len(fam.Prompts), len(fam.Answers))
	}
	for _, a := range fam.Answers {
		if a.Tier != TierFamily {
			t.Fatalf("non-family answer %q in family deck", a.ID)
		}
	}
	for _, p := range fam.Prompts {
		if p.Tier != TierFamily {
			t.Fatalf("non-family prompt %q in family deck", p.ID)
		}
	}

	party := d.For(TierParty)
	if len(party.Answers) <= len(fam.Answers) || len(party.Prompts) <= len(fam.Prompts) {
		t.Fatal("party deck should be a strict superset of the family deck")
	}
}

func TestSeedDeckMeetsProductionVolume(t *testing.T) {
	d := NewSeedDeck()

	if len(d.Prompts) < minSeedPrompts {
		t.Fatalf("seed deck has %d prompts, want at least %d", len(d.Prompts), minSeedPrompts)
	}
	if len(d.Answers) < minSeedAnswers {
		t.Fatalf("seed deck has %d answers, want at least %d", len(d.Answers), minSeedAnswers)
	}
	if got := len(d.For(TierFamily).Answers); got < minFamilyAnswers {
		t.Fatalf("family deck has %d answers, want at least %d", got, minFamilyAnswers)
	}
}

func TestParseSeedDeckRejectsInvalidContent(t *testing.T) {
	_, err := ParseSeedDeck([]byte(`{"prompts":[{"id":"p_1","tier":"weird","text":"____"}],"answers":[]}`))
	if err == nil || !strings.Contains(err.Error(), "need at least") {
		t.Fatalf("ParseSeedDeck err = %v, want volume validation error", err)
	}
}
