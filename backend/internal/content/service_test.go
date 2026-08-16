package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestReportValidation(t *testing.T) {
	s := NewService(nil)
	if s.Available() {
		t.Fatal("a database-less service must report unavailable")
	}
	if _, err := s.Report(context.Background(), ReportInput{}, "seed"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("report without a database = %v", err)
	}
	if !validUUID("00112233-4455-6677-8899-aabbccddeeff") || validUUID("nope") {
		t.Fatal("uuid validation is wrong")
	}
	if clampLimit(0) != 50 || clampLimit(9999) != 200 || clampLimit(10) != 10 {
		t.Fatal("limit clamping is wrong")
	}
}

// A sort the card's table cannot serve has to be rejected as bad input. The
// old code handed "skipped" to answer_cards, which has no skip_count, and the
// admin saw a 503 claiming the content platform was down.
func TestCardSortIsValidatedPerKind(t *testing.T) {
	supported := []struct{ kind, sort, want string }{
		{KindPrompt, "", "c.times_played DESC"},
		{KindPrompt, "unplayed", "c.times_played ASC"},
		{KindPrompt, "reported", "c.report_count DESC, c.times_played DESC"},
		{KindPrompt, "skipped", "c.skip_count DESC"},
		{KindAnswer, "played", "c.times_played DESC"},
		{KindAnswer, "winning", "c.win_count DESC"},
	}
	for _, tc := range supported {
		order, err := cardOrder(tc.kind, tc.sort)
		if err != nil || order != tc.want {
			t.Fatalf("cardOrder(%q, %q) = %q, %v; want %q", tc.kind, tc.sort, order, err, tc.want)
		}
	}

	unsupported := []struct{ kind, sort string }{
		{KindAnswer, "skipped"},
		{KindPrompt, "winning"},
		{KindAnswer, "sideways"},
	}
	for _, tc := range unsupported {
		if _, err := cardOrder(tc.kind, tc.sort); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cardOrder(%q, %q) = %v; want ErrInvalidInput", tc.kind, tc.sort, err)
		}
	}
}

func testService(t *testing.T) (*Service, *sql.DB, context.Context, func()) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	return NewService(db), db, ctx, func() {
		cancel()
		db.Close()
	}
}

// A card with enough distinct reporters leaves rotation without an admin, and
// an admin can put it back.
func TestPostgresReportsAutoRetireThenRestore(t *testing.T) {
	s, db, ctx, done := testService(t)
	defer done()

	var packID string
	if err := db.QueryRowContext(ctx, `SELECT id::text FROM packs WHERE slug = $1`, PackSlugCommunity).Scan(&packID); err != nil {
		t.Fatal(err)
	}
	var cardID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO answer_cards (pack_id, text, tier, source, status)
		VALUES ($1::uuid, $2, 'party', 'community', 'approved') RETURNING id::text`,
		packID, fmt.Sprintf("report fixture %d", time.Now().UnixNano())).Scan(&cardID); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DELETE FROM answer_cards WHERE id = $1::uuid`, cardID)

	input := ReportInput{CardKind: KindAnswer, CardID: cardID, Reason: "offensive", RoomCode: "ABC123"}
	if _, err := s.Report(ctx, ReportInput{CardKind: "sideways", CardID: cardID, Reason: "offensive"}, "a"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad kind = %v", err)
	}
	if _, err := s.Report(ctx, ReportInput{CardKind: KindAnswer, CardID: cardID, Reason: "because"}, "a"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad reason = %v", err)
	}

	// The same reporter filing repeatedly must not move the counter.
	for i := 0; i < 5; i++ {
		result, err := s.Report(ctx, input, "reporter-one")
		if err != nil {
			t.Fatal(err)
		}
		if result.Retired {
			t.Fatal("one reporter retired a card on their own")
		}
	}
	var reportCount int
	if err := db.QueryRowContext(ctx, `SELECT report_count FROM answer_cards WHERE id = $1::uuid`, cardID).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 1 {
		t.Fatalf("report_count = %d after five reports from one client, want 1", reportCount)
	}

	if _, err := s.Report(ctx, input, "reporter-two"); err != nil {
		t.Fatal(err)
	}
	third, err := s.Report(ctx, input, "reporter-three")
	if err != nil {
		t.Fatal(err)
	}
	if !third.Retired {
		t.Fatal("the third distinct reporter should have retired the card")
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status::text FROM answer_cards WHERE id = $1::uuid`, cardID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "retired" {
		t.Fatalf("status = %q, want retired", status)
	}

	reports, err := s.OpenReports(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, report := range reports {
		if report.CardID == cardID {
			found = true
			if report.ReportsFor != 3 {
				t.Fatalf("reports_for_card = %d, want 3", report.ReportsFor)
			}
		}
	}
	if !found {
		t.Fatal("the retired card is missing from the open report queue")
	}

	// Dismissing restores the card and closes every report on it.
	if err := s.ResolveReports(ctx, KindAnswer, cardID, "dismissed"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status::text FROM answer_cards WHERE id = $1::uuid`, cardID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "approved" {
		t.Fatalf("status after dismissal = %q, want approved", status)
	}
	var open int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM card_reports WHERE answer_card_id = $1::uuid AND resolved_at IS NULL`, cardID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("%d reports left open after resolution", open)
	}
	if err := s.ResolveReports(ctx, KindAnswer, cardID, "dismissed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolving an already-clean card = %v", err)
	}
}

// Approving a candidate is the only path from player text to a real card.
func TestPostgresCandidateApprovalCreatesCommunityCard(t *testing.T) {
	s, db, ctx, done := testService(t)
	defer done()

	text := fmt.Sprintf("a candidate answer %d", time.Now().UnixNano())
	var candidateID string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO card_candidates (text, normalized_text, author_name, vote_count)
		VALUES ($1, $2, 'Tester', 4) RETURNING id::text`, text, text).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DELETE FROM card_candidates WHERE id = $1::uuid`, candidateID)

	pending, err := s.PendingCandidates(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("the pending queue is empty")
	}

	if err := s.ReviewCandidate(ctx, candidateID, "sideways", "party"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad decision = %v", err)
	}
	if err := s.ReviewCandidate(ctx, candidateID, "approved", "unfiltered"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unsupported tier = %v", err)
	}
	if err := s.ReviewCandidate(ctx, candidateID, "approved", "party"); err != nil {
		t.Fatal(err)
	}

	var cardID, status, pack, source string
	if err := db.QueryRowContext(ctx, `
		SELECT c.id::text, c.status::text, p.slug, c.source::text
		FROM card_candidates cc
		JOIN answer_cards c ON c.id = cc.promoted_answer_card_id
		JOIN packs p ON p.id = c.pack_id
		WHERE cc.id = $1::uuid`, candidateID).Scan(&cardID, &status, &pack, &source); err != nil {
		t.Fatalf("promoted card lookup: %v", err)
	}
	defer db.ExecContext(context.Background(), `DELETE FROM answer_cards WHERE id = $1::uuid`, cardID)
	if status != "approved" || pack != PackSlugCommunity || source != "community" {
		t.Fatalf("promoted card = %s/%s/%s", status, pack, source)
	}

	// Reviewing twice must not mint a second card.
	if err := s.ReviewCandidate(ctx, candidateID, "approved", "party"); !errors.Is(err, ErrConflict) {
		t.Fatalf("double review = %v", err)
	}
}

func TestPostgresCardBrowserSortsAndFilters(t *testing.T) {
	s, _, ctx, done := testService(t)
	defer done()

	if _, err := s.Cards(ctx, "sideways", "", "", 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad kind = %v", err)
	}
	if _, err := s.Cards(ctx, KindAnswer, "", "sideways", 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad sort = %v", err)
	}
	if _, err := s.Cards(ctx, KindAnswer, "approved", "skipped", 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("answers sorted by skips = %v", err)
	}
	cards, err := s.Cards(ctx, KindAnswer, "approved", "played", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) == 0 {
		t.Fatal("no approved answer cards")
	}
	for i := 1; i < len(cards); i++ {
		if cards[i].TimesPlayed > cards[i-1].TimesPlayed {
			t.Fatal("played sort is not descending")
		}
	}
	for _, card := range cards {
		if card.Status != "approved" || card.Kind != KindAnswer {
			t.Fatalf("filter leaked: %+v", card)
		}
	}

	prompts, err := s.Cards(ctx, KindPrompt, "", "skipped", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) == 0 {
		t.Fatal("no prompt cards")
	}

	overview, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if overview.ApprovedPrompts == 0 || overview.AutoRetireAt != autoRetireThreshold {
		t.Fatalf("overview = %+v", overview)
	}
}
