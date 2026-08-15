package cards

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// PackSlugOfficial is the launch pack seeded by migration 005. Rooms draw from
// it until pack selection exists.
const PackSlugOfficial = "official-core"

// Queryer is the read surface the loader needs. Both *sql.DB and *sql.Tx
// satisfy it, which lets tests stage content changes in a transaction they roll
// back instead of mutating shared data.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadDeckFromDB reads the playable deck out of Postgres: approved cards that
// belong to approved packs. It applies the same validation as the seed file, so
// a half-migrated or over-moderated database fails loudly here rather than
// dealing a broken hand mid-game.
//
// Cards at tiers the engine cannot filter yet (currently 'unfiltered') are left
// out on purpose: Deck.For treats any non-family tier as "everything", so
// including them would leak them into party rooms.
func LoadDeckFromDB(ctx context.Context, db Queryer) (Deck, error) {
	if db == nil {
		return Deck{}, fmt.Errorf("load deck from database: no database handle")
	}
	prompts, err := loadPrompts(ctx, db)
	if err != nil {
		return Deck{}, err
	}
	answers, err := loadAnswers(ctx, db)
	if err != nil {
		return Deck{}, err
	}
	deck := Deck{Prompts: prompts, Answers: answers}
	if err := validateDeck(&deck); err != nil {
		return Deck{}, fmt.Errorf("database deck is not playable: %w", err)
	}
	return deck, nil
}

const playableCardFilter = `
	WHERE c.status = 'approved'
	  AND p.status = 'approved'
	  AND c.tier IN ('family', 'party')
	  AND length(btrim(c.text)) > 0
	ORDER BY c.external_id NULLS LAST, c.id`

func loadPrompts(ctx context.Context, db Queryer) ([]PromptCard, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id::text, COALESCE(c.external_id, c.id::text), c.text, c.tier::text
		FROM prompt_cards c
		JOIN packs p ON p.id = c.pack_id`+playableCardFilter)
	if err != nil {
		return nil, fmt.Errorf("query prompt cards: %w", err)
	}
	defer rows.Close()

	out := []PromptCard{}
	for rows.Next() {
		var card PromptCard
		if err := rows.Scan(&card.UUID, &card.ID, &card.Text, &card.Tier); err != nil {
			return nil, fmt.Errorf("scan prompt card: %w", err)
		}
		card.Text = strings.TrimSpace(card.Text)
		out = append(out, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read prompt cards: %w", err)
	}
	return out, nil
}

func loadAnswers(ctx context.Context, db Queryer) ([]AnswerCard, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id::text, COALESCE(c.external_id, c.id::text), c.text, c.tier::text
		FROM answer_cards c
		JOIN packs p ON p.id = c.pack_id`+playableCardFilter)
	if err != nil {
		return nil, fmt.Errorf("query answer cards: %w", err)
	}
	defer rows.Close()

	out := []AnswerCard{}
	for rows.Next() {
		var card AnswerCard
		if err := rows.Scan(&card.UUID, &card.ID, &card.Text, &card.Tier); err != nil {
			return nil, fmt.Errorf("scan answer card: %w", err)
		}
		card.Text = strings.TrimSpace(card.Text)
		out = append(out, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read answer cards: %w", err)
	}
	return out, nil
}
