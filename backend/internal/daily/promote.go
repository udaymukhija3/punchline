package daily

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode"
)

// The content pipeline: daily rounds are a continuous writers' room. When a
// round finalizes, the answers the group actually voted for become candidate
// cards for an admin to promote into the community pack. Every card in that
// pack was therefore written by a player and play-tested by their friends.
const (
	// minCandidateVotes keeps one-vote flukes and two-person groups out of the
	// queue. A card needs a real plurality behind it.
	minCandidateVotes = 2
	// maxCandidatesPerRound caps a single round's contribution so one very
	// large group cannot flood the review queue.
	maxCandidatesPerRound = 3
)

// promoteRoundCandidates harvests the winning answers of a finalized round.
// It is best-effort by design: a failure here must never block finalization,
// because the round result is what players are waiting on.
//
// Candidates default to the party tier. Player-written text is never assumed
// safe for family rooms, and Deck.For(family) only admits family cards, so an
// unreviewed candidate cannot reach a family room even if it is promoted.
func (s *Service) promoteRoundCandidates(ctx context.Context, roundID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.answer_text, m.display_name, count(v.submission_id)::int AS votes
		FROM daily_submissions s
		JOIN daily_memberships m ON m.id = s.membership_id
		LEFT JOIN daily_votes v ON v.submission_id = s.id
		WHERE s.daily_round_id = $1::uuid
		GROUP BY s.id, m.display_name
		HAVING count(v.submission_id) >= $2
		ORDER BY votes DESC, s.submitted_at
		LIMIT $3`, roundID, minCandidateVotes, maxCandidatesPerRound)
	if err != nil {
		return 0, fmt.Errorf("load candidate answers: %w", err)
	}
	type candidate struct {
		text, author string
		votes        int
	}
	found := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.text, &c.author, &c.votes); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan candidate answer: %w", err)
		}
		found = append(found, c)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	promoted := 0
	for _, c := range found {
		text := strings.TrimSpace(c.text)
		normalized := NormalizeCandidateText(text)
		if normalized == "" {
			continue
		}
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO card_candidates (text, normalized_text, author_name, vote_count, source_round_id)
			VALUES ($1, $2, $3, $4, $5::uuid)
			ON CONFLICT (normalized_text) DO UPDATE
			SET vote_count = GREATEST(card_candidates.vote_count, EXCLUDED.vote_count)
			WHERE card_candidates.status = 'pending'`,
			text, normalized, c.author, c.votes, roundID)
		if err != nil {
			return promoted, fmt.Errorf("insert card candidate: %w", err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected > 0 {
			promoted++
		}
	}
	return promoted, nil
}

// NormalizeCandidateText produces the dedupe key for candidate answers: the
// same joke typed with different capitalisation, spacing, or trailing
// punctuation collapses to one queue entry.
func NormalizeCandidateText(value string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		}
		// Punctuation is dropped entirely.
	}
	return strings.TrimSpace(b.String())
}

// finalizeRounds transitions due rounds to FINALIZED and harvests candidates
// from each one. It returns the number of rounds finalized.
func (s *Service) finalizeRounds(ctx context.Context, groupClause string, args []any) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE daily_rounds SET state = 'FINALIZED', finalized_at = $1, updated_at = now()
		WHERE state = 'REVEALED_FOR_VOTING' AND voting_closes_at <= $1`+groupClause+`
		RETURNING id::text`, args...)
	if err != nil {
		return 0, fmt.Errorf("finalize due daily rounds: %w", err)
	}
	finalized := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan finalized daily round: %w", err)
		}
		finalized = append(finalized, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, roundID := range finalized {
		if _, err := s.promoteRoundCandidates(ctx, roundID); err != nil {
			// Losing a candidate is not worth failing a finalized round over:
			// the result is what players are waiting on.
			log.Printf("daily candidate promotion for round %s: %v", roundID, err)
		}
	}
	return len(finalized), nil
}
