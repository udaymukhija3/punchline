package telemetry

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestNilRecorderAndEmptyUUIDsAreSafe(t *testing.T) {
	var nilRecorder *CardRecorder
	nilRecorder.PromptPlayed("x")
	nilRecorder.AnswerWon("y")
	nilRecorder.Start(context.Background(), time.Second)
	if flushed, dropped, failed := nilRecorder.Stats(); flushed|dropped|failed != 0 {
		t.Fatal("nil recorder reported activity")
	}
	if NewCardRecorder(nil, 8) != nil {
		t.Fatal("expected a nil recorder without a database")
	}
}

func TestRecorderDropsInsteadOfBlocking(t *testing.T) {
	// A recorder whose flusher never runs must still accept calls forever.
	r := &CardRecorder{db: &sql.DB{}, events: make(chan event, 2)}
	for i := 0; i < 50; i++ {
		r.AnswerPlayed("11111111-1111-4111-8111-111111111111")
	}
	_, dropped, _ := r.Stats()
	if dropped != 48 {
		t.Fatalf("dropped = %d, want 48", dropped)
	}
}

func TestPostgresFlushAggregatesCounts(t *testing.T) {
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

	var promptID, answerID string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM prompt_cards ORDER BY external_id LIMIT 1`).Scan(&promptID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM answer_cards ORDER BY external_id LIMIT 1`).Scan(&answerID); err != nil {
		t.Fatal(err)
	}
	var basePlays, baseSkips, baseWins int
	if err := db.QueryRowContext(ctx, `SELECT times_played, skip_count FROM prompt_cards WHERE id = $1::uuid`, promptID).Scan(&basePlays, &baseSkips); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT win_count FROM answer_cards WHERE id = $1::uuid`, answerID).Scan(&baseWins); err != nil {
		t.Fatal(err)
	}

	recorder := NewCardRecorder(db, 64)
	for i := 0; i < 3; i++ {
		recorder.PromptPlayed(promptID)
	}
	recorder.PromptSkipped(promptID)
	recorder.AnswerWon(answerID)
	recorder.PromptPlayed("") // seed-backed card, must be ignored

	pending := map[event]int{}
	recorder.drain(pending)
	recorder.flush(ctx, pending)

	var plays, skips, wins int
	if err := db.QueryRowContext(ctx, `SELECT times_played, skip_count FROM prompt_cards WHERE id = $1::uuid`, promptID).Scan(&plays, &skips); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT win_count FROM answer_cards WHERE id = $1::uuid`, answerID).Scan(&wins); err != nil {
		t.Fatal(err)
	}
	if plays != basePlays+3 {
		t.Fatalf("times_played = %d, want %d (three events must aggregate into one write)", plays, basePlays+3)
	}
	if skips != baseSkips+1 {
		t.Fatalf("skip_count = %d, want %d", skips, baseSkips+1)
	}
	if wins != baseWins+1 {
		t.Fatalf("win_count = %d, want %d", wins, baseWins+1)
	}
	if flushed, _, failed := recorder.Stats(); flushed != 5 || failed != 0 {
		t.Fatalf("stats flushed=%d failed=%d, want 5/0", flushed, failed)
	}

	// Leave the fixture as we found it.
	if _, err := db.ExecContext(ctx, `UPDATE prompt_cards SET times_played = $2, skip_count = $3 WHERE id = $1::uuid`, promptID, basePlays, baseSkips); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE answer_cards SET win_count = $2 WHERE id = $1::uuid`, answerID, baseWins); err != nil {
		t.Fatal(err)
	}
}
