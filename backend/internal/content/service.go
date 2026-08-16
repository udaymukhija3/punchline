// Package content is the moderation and curation half of the card platform:
// player reports, the review queues behind the admin desk, and promotion of
// player-written candidates into the community pack.
package content

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// autoRetireThreshold is how many distinct reporters it takes to pull a card
// out of rotation automatically. Reports are deduped per reporter, so this is
// a count of people, not clicks. An admin can restore the card afterwards; the
// point is that a genuinely offensive card stops being dealt within one game
// rather than waiting for someone to check a queue.
const autoRetireThreshold = 3

var validReasons = map[string]bool{
	"offensive": true, "broken": true, "unfunny": true, "duplicate": true, "other": true,
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Available() bool { return s != nil && s.db != nil }

// Report files a player report. Repeat reports from the same client update the
// existing row rather than stacking, so the retire threshold counts people.
func (s *Service) Report(ctx context.Context, input ReportInput, reporterSeed string) (ReportResult, error) {
	if !s.Available() {
		return ReportResult{}, ErrUnavailable
	}
	kind := strings.TrimSpace(input.CardKind)
	if kind != KindPrompt && kind != KindAnswer {
		return ReportResult{}, ErrInvalidInput
	}
	if !validUUID(input.CardID) || !validReasons[input.Reason] {
		return ReportResult{}, ErrInvalidInput
	}
	detail := strings.TrimSpace(input.Detail)
	if utf8.RuneCountInString(detail) > 280 {
		detail = string([]rune(detail)[:280])
	}
	roomCode := strings.ToUpper(strings.TrimSpace(input.RoomCode))
	if len(roomCode) > 12 {
		roomCode = roomCode[:12]
	}
	digest := sha256.Sum256([]byte(reporterSeed + "|" + input.CardID))

	column := "prompt_card_id"
	table := "prompt_cards"
	if kind == KindAnswer {
		column = "answer_card_id"
		table = "answer_cards"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReportResult{}, fmt.Errorf("begin card report: %w", err)
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = $1::uuid)`, table), input.CardID).Scan(&exists); err != nil {
		return ReportResult{}, fmt.Errorf("check reported card: %w", err)
	}
	if !exists {
		return ReportResult{}, ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO card_reports (%s, reason, detail, reporter_hash, room_code)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT ((COALESCE(prompt_card_id, answer_card_id)), reporter_hash) DO UPDATE
		SET reason = EXCLUDED.reason, detail = EXCLUDED.detail`, column),
		input.CardID, input.Reason, detail, digest[:], roomCode); err != nil {
		return ReportResult{}, fmt.Errorf("insert card report: %w", err)
	}

	var openReports int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT count(*) FROM card_reports
		WHERE %s = $1::uuid AND resolved_at IS NULL`, column), input.CardID).Scan(&openReports); err != nil {
		return ReportResult{}, fmt.Errorf("count card reports: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET report_count = $2 WHERE id = $1::uuid`, table), input.CardID, openReports); err != nil {
		return ReportResult{}, fmt.Errorf("update card report count: %w", err)
	}

	retired := false
	if openReports >= autoRetireThreshold {
		result, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET status = 'retired' WHERE id = $1::uuid AND status = 'approved'`, table), input.CardID)
		if err != nil {
			return ReportResult{}, fmt.Errorf("auto-retire card: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			retired = true
		}
	}
	if err := tx.Commit(); err != nil {
		return ReportResult{}, fmt.Errorf("commit card report: %w", err)
	}
	return ReportResult{Recorded: true, Retired: retired}, nil
}

// OpenReports lists unresolved reports, worst first.
func (s *Service) OpenReports(ctx context.Context, limit int) ([]Report, error) {
	if !s.Available() {
		return nil, ErrUnavailable
	}
	limit = clampLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id::text, 'prompt', c.id::text, c.text, c.status::text, r.reason, r.detail, r.room_code, c.report_count, r.created_at
		FROM card_reports r JOIN prompt_cards c ON c.id = r.prompt_card_id
		WHERE r.resolved_at IS NULL
		UNION ALL
		SELECT r.id::text, 'answer', c.id::text, c.text, c.status::text, r.reason, r.detail, r.room_code, c.report_count, r.created_at
		FROM card_reports r JOIN answer_cards c ON c.id = r.answer_card_id
		WHERE r.resolved_at IS NULL
		ORDER BY report_count DESC, created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list card reports: %w", err)
	}
	defer rows.Close()
	out := []Report{}
	for rows.Next() {
		var item Report
		if err := rows.Scan(&item.ID, &item.CardKind, &item.CardID, &item.CardText, &item.CardStatus,
			&item.Reason, &item.Detail, &item.RoomCode, &item.ReportsFor, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan card report: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ResolveReports closes every open report on a card and either retires or
// restores it. Resolving per card rather than per report matches how the work
// is actually done: you judge the card once.
func (s *Service) ResolveReports(ctx context.Context, kind, cardID, resolution string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !validUUID(cardID) || (kind != KindPrompt && kind != KindAnswer) {
		return ErrInvalidInput
	}
	if resolution != "retired" && resolution != "dismissed" {
		return ErrInvalidInput
	}
	column, table := "prompt_card_id", "prompt_cards"
	if kind == KindAnswer {
		column, table = "answer_card_id", "answer_cards"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin report resolution: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE card_reports SET resolved_at = now(), resolution = $2::card_report_resolution
		WHERE %s = $1::uuid AND resolved_at IS NULL`, column), cardID, resolution)
	if err != nil {
		return fmt.Errorf("resolve card reports: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	status := "approved"
	if resolution == "retired" {
		status = "retired"
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = $2::card_status, report_count = 0 WHERE id = $1::uuid`, table), cardID, status); err != nil {
		return fmt.Errorf("apply report resolution: %w", err)
	}
	return tx.Commit()
}

// PendingCandidates lists player answers waiting to become cards, best first.
func (s *Service) PendingCandidates(ctx context.Context, limit int) ([]Candidate, error) {
	if !s.Available() {
		return nil, ErrUnavailable
	}
	limit = clampLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id::text, text, author_name, vote_count, status::text, created_at
		FROM card_candidates WHERE status = 'pending'
		ORDER BY vote_count DESC, created_at
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list card candidates: %w", err)
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		var item Candidate
		if err := rows.Scan(&item.ID, &item.Text, &item.AuthorName, &item.VoteCount, &item.Status, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan card candidate: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ReviewCandidate approves a candidate into the community pack, or rejects it.
// Approval is the only path by which player-written text becomes a real card.
func (s *Service) ReviewCandidate(ctx context.Context, candidateID, decision, tier string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !validUUID(candidateID) || (decision != "approved" && decision != "rejected") {
		return ErrInvalidInput
	}
	if tier == "" {
		tier = "party"
	}
	if tier != "party" && tier != "family" {
		return ErrInvalidInput
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate review: %w", err)
	}
	defer tx.Rollback()

	var text, status string
	err = tx.QueryRowContext(ctx, `
		SELECT text, status::text FROM card_candidates WHERE id = $1::uuid FOR UPDATE`, candidateID).Scan(&text, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load card candidate: %w", err)
	}
	if status != "pending" {
		return ErrConflict
	}

	if decision == "rejected" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE card_candidates SET status = 'rejected', reviewed_at = now() WHERE id = $1::uuid`, candidateID); err != nil {
			return fmt.Errorf("reject card candidate: %w", err)
		}
		return tx.Commit()
	}

	var cardID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO answer_cards (pack_id, text, tier, source, status)
		SELECT p.id, $2, $3::content_tier, 'community', 'approved'
		FROM packs p WHERE p.slug = $1
		RETURNING id::text`, PackSlugCommunity, text, tier).Scan(&cardID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("community pack %q is missing", PackSlugCommunity)
	}
	if err != nil {
		return fmt.Errorf("promote card candidate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE card_candidates
		SET status = 'approved', reviewed_at = now(), promoted_answer_card_id = $2::uuid
		WHERE id = $1::uuid`, candidateID, cardID); err != nil {
		return fmt.Errorf("record candidate promotion: %w", err)
	}
	return tx.Commit()
}

// Cards lists cards for the admin browser. sort selects the curation lens:
// "reported" surfaces complaints, "skipped" and "unplayed" surface duds,
// "winning" surfaces keepers. Not every lens fits both kinds; see cardOrder.
func (s *Service) Cards(ctx context.Context, kind, status, sort string, limit int) ([]Card, error) {
	if !s.Available() {
		return nil, ErrUnavailable
	}
	if kind != KindPrompt && kind != KindAnswer {
		return nil, ErrInvalidInput
	}
	limit = clampLimit(limit)
	statusClause := ""
	args := []any{limit}
	if status != "" {
		switch status {
		case "approved", "retired", "draft", "rejected":
			statusClause = " AND c.status = $2::card_status"
			args = append(args, status)
		default:
			return nil, ErrInvalidInput
		}
	}

	table, performance := "prompt_cards", "c.skip_count"
	if kind == KindAnswer {
		table, performance = "answer_cards", "c.win_count"
	}
	order, err := cardOrder(kind, sort)
	if err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`
		SELECT c.id::text, COALESCE(c.external_id, ''), c.text, c.tier::text, c.status::text, p.slug,
		       c.times_played, %s, c.report_count
		FROM %s c JOIN packs p ON p.id = c.pack_id
		WHERE true%s
		ORDER BY %s, c.id
		LIMIT $1`, performance, table, statusClause, order)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	defer rows.Close()

	out := []Card{}
	for rows.Next() {
		var card Card
		var perf int
		if err := rows.Scan(&card.ID, &card.ExternalID, &card.Text, &card.Tier, &card.Status, &card.Pack,
			&card.TimesPlayed, &perf, &card.ReportCount); err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		card.Kind = kind
		if kind == KindAnswer {
			card.WinCount = perf
			if card.TimesPlayed > 0 {
				card.WinRate = float64(perf) / float64(card.TimesPlayed)
			}
		} else {
			card.SkipCount = perf
			if card.TimesPlayed > 0 {
				card.SkipRate = float64(perf) / float64(card.TimesPlayed)
			}
		}
		out = append(out, card)
	}
	return out, rows.Err()
}

// cardOrder resolves a curation lens to an ORDER BY clause. The two card
// tables count different things — only prompt_cards has skip_count, only
// answer_cards has win_count — so a lens the kind cannot serve is a bad
// request. Letting it reach Postgres turns a typo into a 503 that reads as an
// outage.
func cardOrder(kind, sort string) (string, error) {
	switch sort {
	case "", "played":
		return "c.times_played DESC", nil
	case "unplayed":
		return "c.times_played ASC", nil
	case "reported":
		return "c.report_count DESC, c.times_played DESC", nil
	case "skipped":
		if kind == KindPrompt {
			return "c.skip_count DESC", nil
		}
	case "winning":
		if kind == KindAnswer {
			return "c.win_count DESC", nil
		}
	}
	return "", ErrInvalidInput
}

// SetCardStatus retires or restores a card directly from the browser.
func (s *Service) SetCardStatus(ctx context.Context, kind, cardID, status string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !validUUID(cardID) || (kind != KindPrompt && kind != KindAnswer) {
		return ErrInvalidInput
	}
	if status != "approved" && status != "retired" {
		return ErrInvalidInput
	}
	table := "prompt_cards"
	if kind == KindAnswer {
		table = "answer_cards"
	}
	result, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET status = $2::card_status WHERE id = $1::uuid`, table), cardID, status)
	if err != nil {
		return fmt.Errorf("set card status: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDailySubmission removes a single player's answer from a daily round.
// This is the moderation lever for user-written text: the product has no
// accounts, so there is no user to ban, only content to remove.
func (s *Service) DeleteDailySubmission(ctx context.Context, submissionID string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !validUUID(submissionID) {
		return ErrInvalidInput
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM daily_submissions WHERE id = $1::uuid`, submissionID)
	if err != nil {
		return fmt.Errorf("delete daily submission: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Overview powers the dashboard header.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	if !s.Available() {
		return Overview{}, ErrUnavailable
	}
	var o Overview
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM card_reports WHERE resolved_at IS NULL),
			(SELECT count(*) FROM card_candidates WHERE status = 'pending'),
			(SELECT count(*) FROM prompt_cards WHERE status = 'approved'),
			(SELECT count(*) FROM answer_cards WHERE status = 'approved'),
			(SELECT count(*) FROM prompt_cards WHERE status = 'retired')
				+ (SELECT count(*) FROM answer_cards WHERE status = 'retired'),
			(SELECT count(*) FROM card_candidates WHERE status = 'approved'),
			(SELECT count(*) FROM daily_groups),
			(SELECT count(*) FROM daily_rounds WHERE round_date = current_date),
			(SELECT count(*) FROM daily_memberships WHERE last_seen_at > now() - interval '7 days')`).Scan(
		&o.OpenReports, &o.PendingCandidates, &o.ApprovedPrompts, &o.ApprovedAnswers,
		&o.RetiredCards, &o.CommunityPromoted, &o.DailyGroups, &o.DailyRoundsToday, &o.ActiveDailyMembers)
	if err != nil {
		return Overview{}, fmt.Errorf("load content overview: %w", err)
	}
	o.AutoRetireAt = autoRetireThreshold
	return o, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
