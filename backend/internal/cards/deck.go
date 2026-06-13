package cards

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// Content tiers. A room set to "family" draws only family-rated cards; "party"
// (the default) draws everything. Tiers are ordered family ⊂ party.
const (
	TierFamily = "family"
	TierParty  = "party"
)

const (
	defaultSeedPath     = "seed/cards.json"
	minSeedPrompts      = 90
	minSeedAnswers      = 180
	minFamilyPrompts    = 50
	minFamilyAnswers    = 100
	seedDeckPathEnvName = "PUNCHLINE_SEED_CARDS"
)

type PromptCard struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Tier string `json:"tier"`
}

type AnswerCard struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Tier string `json:"tier"`
}

type Deck struct {
	Prompts []PromptCard `json:"prompts"`
	Answers []AnswerCard `json:"answers"`
}

// For returns the subset of the deck allowed at the given content tier. A
// family room sees only family cards; any other tier sees the full deck.
func (d Deck) For(tier string) Deck {
	if tier != TierFamily {
		return d
	}
	out := Deck{}
	for _, p := range d.Prompts {
		if p.Tier == TierFamily {
			out.Prompts = append(out.Prompts, p)
		}
	}
	for _, a := range d.Answers {
		if a.Tier == TierFamily {
			out.Answers = append(out.Answers, a)
		}
	}
	return out
}

// ShuffledPrompts returns a fresh shuffled copy of the prompt pile.
func (d Deck) ShuffledPrompts() []PromptCard {
	out := make([]PromptCard, len(d.Prompts))
	copy(out, d.Prompts)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// ShuffledAnswers returns a fresh shuffled copy of the answer pile.
func (d Deck) ShuffledAnswers() []AnswerCard {
	out := make([]AnswerCard, len(d.Answers))
	copy(out, d.Answers)
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func NewSeedDeck() Deck {
	path, err := FindSeedDeckPath()
	if err != nil {
		panic(err)
	}
	d, err := LoadSeedDeck(path)
	if err != nil {
		panic(err)
	}
	return d
}

func FindSeedDeckPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv(seedDeckPathEnvName)); path != "" {
		return path, nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, defaultSeedPath)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("%s not found from current directory; set %s to override", defaultSeedPath, seedDeckPathEnvName)
}

func LoadSeedDeck(path string) (Deck, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Deck{}, err
	}
	return ParseSeedDeck(data)
}

func ParseSeedDeck(data []byte) (Deck, error) {
	var d Deck
	if err := json.Unmarshal(data, &d); err != nil {
		return Deck{}, err
	}
	if err := validateDeck(&d); err != nil {
		return Deck{}, err
	}
	return d, nil
}

func validateDeck(d *Deck) error {
	if len(d.Prompts) < minSeedPrompts {
		return fmt.Errorf("seed deck has %d prompts; need at least %d", len(d.Prompts), minSeedPrompts)
	}
	if len(d.Answers) < minSeedAnswers {
		return fmt.Errorf("seed deck has %d answers; need at least %d", len(d.Answers), minSeedAnswers)
	}

	familyPrompts, err := validatePrompts(d.Prompts)
	if err != nil {
		return err
	}
	familyAnswers, err := validateAnswers(d.Answers)
	if err != nil {
		return err
	}
	if familyPrompts < minFamilyPrompts {
		return fmt.Errorf("seed deck has %d family prompts; need at least %d", familyPrompts, minFamilyPrompts)
	}
	if familyAnswers < minFamilyAnswers {
		return fmt.Errorf("seed deck has %d family answers; need at least %d", familyAnswers, minFamilyAnswers)
	}
	return nil
}

func validatePrompts(cards []PromptCard) (int, error) {
	seen := map[string]bool{}
	family := 0
	for i := range cards {
		cards[i].ID = strings.TrimSpace(cards[i].ID)
		cards[i].Text = strings.TrimSpace(cards[i].Text)
		tier, err := normalizeSeedTier(cards[i].Tier)
		if err != nil {
			return 0, fmt.Errorf("prompt %q: %w", cards[i].ID, err)
		}
		cards[i].Tier = tier
		if cards[i].ID == "" || cards[i].Text == "" {
			return 0, errors.New("prompt cards must have id and text")
		}
		if seen[cards[i].ID] {
			return 0, fmt.Errorf("duplicate prompt id %q", cards[i].ID)
		}
		seen[cards[i].ID] = true
		if tier == TierFamily {
			family++
		}
	}
	return family, nil
}

func validateAnswers(cards []AnswerCard) (int, error) {
	seen := map[string]bool{}
	family := 0
	for i := range cards {
		cards[i].ID = strings.TrimSpace(cards[i].ID)
		cards[i].Text = strings.TrimSpace(cards[i].Text)
		tier, err := normalizeSeedTier(cards[i].Tier)
		if err != nil {
			return 0, fmt.Errorf("answer %q: %w", cards[i].ID, err)
		}
		cards[i].Tier = tier
		if cards[i].ID == "" || cards[i].Text == "" {
			return 0, errors.New("answer cards must have id and text")
		}
		if seen[cards[i].ID] {
			return 0, fmt.Errorf("duplicate answer id %q", cards[i].ID)
		}
		seen[cards[i].ID] = true
		if tier == TierFamily {
			family++
		}
	}
	return family, nil
}

func normalizeSeedTier(tier string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", TierParty:
		return TierParty, nil
	case TierFamily:
		return TierFamily, nil
	default:
		return "", fmt.Errorf("unknown tier %q", tier)
	}
}
