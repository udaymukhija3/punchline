package cards

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestLoadDeckFromDBRequiresAHandle(t *testing.T) {
	if _, err := LoadDeckFromDB(context.Background(), nil); err == nil {
		t.Fatal("expected an error without a database handle")
	}
}

// The database deck must be interchangeable with the seed deck: same content,
// same stable ids, same tier split. If it is not, rooms restored from a
// snapshot written by a seed-backed instance would disagree with a
// database-backed one about what a card id means.
func TestPostgresDeckMatchesSeedDeckAndCarriesRowIDs(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	dbDeck, err := LoadDeckFromDB(ctx, db)
	if err != nil {
		t.Fatalf("load deck from database: %v", err)
	}
	seedDeck := NewSeedDeck()

	if len(dbDeck.Prompts) != len(seedDeck.Prompts) || len(dbDeck.Answers) != len(seedDeck.Answers) {
		t.Fatalf("deck size = %d/%d, seed = %d/%d",
			len(dbDeck.Prompts), len(dbDeck.Answers), len(seedDeck.Prompts), len(seedDeck.Answers))
	}

	seedPrompts := map[string]PromptCard{}
	for _, card := range seedDeck.Prompts {
		seedPrompts[card.ID] = card
	}
	for _, card := range dbDeck.Prompts {
		seed, ok := seedPrompts[card.ID]
		if !ok {
			t.Fatalf("database prompt %q is not in the seed deck", card.ID)
		}
		if card.Text != seed.Text || card.Tier != seed.Tier {
			t.Fatalf("prompt %q drifted: %+v vs seed %+v", card.ID, card, seed)
		}
		if card.UUID == "" {
			t.Fatalf("prompt %q has no database row id", card.ID)
		}
	}

	seedAnswers := map[string]AnswerCard{}
	for _, card := range seedDeck.Answers {
		seedAnswers[card.ID] = card
	}
	for _, card := range dbDeck.Answers {
		seed, ok := seedAnswers[card.ID]
		if !ok {
			t.Fatalf("database answer %q is not in the seed deck", card.ID)
		}
		if card.Text != seed.Text || card.Tier != seed.Tier {
			t.Fatalf("answer %q drifted: %+v vs seed %+v", card.ID, card, seed)
		}
		if card.UUID == "" {
			t.Fatalf("answer %q has no database row id", card.ID)
		}
	}

	family := dbDeck.For(TierFamily)
	if len(family.Prompts) == 0 || len(family.Prompts) == len(dbDeck.Prompts) {
		t.Fatalf("family filter kept %d of %d prompts", len(family.Prompts), len(dbDeck.Prompts))
	}
	for _, card := range family.Prompts {
		if card.Tier != TierFamily {
			t.Fatalf("family deck contains a %s prompt: %+v", card.Tier, card)
		}
	}
	for _, card := range family.Answers {
		if card.Tier != TierFamily {
			t.Fatalf("family deck contains a %s answer: %+v", card.Tier, card)
		}
	}
}

// Retiring cards must not be able to leave the room engine with an unplayable
// deck: the loader validates minimums and fails instead.
func TestPostgresDeckRejectsOverRetiredContent(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Reading through the transaction sees the staged retirements; rolling back
	// leaves the shared database untouched.
	if _, err := LoadDeckFromDB(ctx, tx); err != nil {
		t.Fatalf("baseline load inside the transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE prompt_cards SET status = 'retired'
		WHERE external_id IN (SELECT external_id FROM prompt_cards ORDER BY external_id LIMIT 40)`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeckFromDB(ctx, tx); err == nil {
		t.Fatal("expected the loader to reject a deck thinned below the playable minimum")
	}

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeckFromDB(ctx, db); err != nil {
		t.Fatalf("deck should be playable again after rollback: %v", err)
	}
}

// A pack pulled from circulation takes its cards with it.
func TestPostgresDeckIgnoresUnapprovedPacks(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE packs SET status = 'draft' WHERE slug = $1`, PackSlugOfficial); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeckFromDB(ctx, tx); err == nil {
		t.Fatal("expected cards from a draft pack to be excluded")
	}
}
