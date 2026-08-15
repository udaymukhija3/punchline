package daily

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"punchline/backend/internal/cards"
)

func TestValidationAndStablePromptSelection(t *testing.T) {
	service := NewService(nil, cards.NewSeedDeck())
	service.db = nil
	if _, _, _, err := validateCreateInput(CreateGroupInput{Name: "A", PlayerName: "Sam", Timezone: "UTC"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short group name error = %v", err)
	}
	if _, _, _, err := validateCreateInput(CreateGroupInput{Name: "Friends", PlayerName: "Sam", Timezone: "Not/AZone"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("timezone error = %v", err)
	}
	first := service.promptFor("group", "2026-08-11")
	second := service.promptFor("group", "2026-08-11")
	if first.ID == "" || first != second {
		t.Fatalf("prompt selection is not stable: %+v %+v", first, second)
	}
	if validUUID("not-a-uuid") || !validUUID("00112233-4455-6677-8899-aabbccddeeff") {
		t.Fatal("UUID validation returned the wrong result")
	}
}

func TestPostgresDailyLifecyclePrivacyIdempotencyAndConcurrentWorker(t *testing.T) {
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
	service := NewService(db, cards.NewSeedDeck())
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	host, err := service.CreateGroup(ctx, CreateGroupInput{Name: "Integration Crew", PlayerName: "Host", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	defer func() {
		if !deleted {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM daily_groups WHERE id = $1::uuid`, host.Group.ID)
		}
	}()
	if host.Token == "" || host.Group.Code == "" {
		t.Fatalf("incomplete host session: %+v", host)
	}
	var storedToken string
	if err := db.QueryRowContext(ctx, `SELECT encode(token_hash, 'hex') FROM daily_memberships WHERE id = $1::uuid`, host.Membership.ID).Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if storedToken == host.Token {
		t.Fatal("daily token was stored in plaintext")
	}

	bob, err := service.JoinGroup(ctx, host.Group.Code, JoinGroupInput{PlayerName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.JoinGroup(ctx, host.Group.Code, JoinGroupInput{PlayerName: "bob"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate case-insensitive name error = %v", err)
	}
	if _, err := service.Today(ctx, host.Group.Code, "wrong-token"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized today error = %v", err)
	}

	hostToday, err := service.Today(ctx, host.Group.Code, host.Token)
	if err != nil {
		t.Fatal(err)
	}
	roundID := hostToday.Today.ID
	if err := service.Submit(ctx, roundID, host.Token, "Host's first answer"); err != nil {
		t.Fatal(err)
	}
	if err := service.Submit(ctx, roundID, host.Token, "Host's updated answer"); err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	if err := service.Submit(ctx, roundID, bob.Token, "Bob's answer"); err != nil {
		t.Fatal(err)
	}
	bobOpen, err := service.Today(ctx, host.Group.Code, bob.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobOpen.Today.Submissions) != 0 || bobOpen.Today.MySubmission.AnswerText != "Bob's answer" {
		t.Fatalf("open round leaked submissions: %+v", bobOpen.Today)
	}

	now = time.Date(2026, 8, 11, 19, 0, 0, 0, time.UTC)
	if _, err := service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	voting, err := service.Today(ctx, host.Group.Code, host.Token)
	if err != nil {
		t.Fatal(err)
	}
	if voting.Today.State != StateVoting || len(voting.Today.Submissions) != 2 {
		t.Fatalf("voting view = %+v", voting.Today)
	}
	for _, submission := range voting.Today.Submissions {
		if submission.AuthorName != "" || submission.VoteCount != 0 {
			t.Fatalf("voting view leaked result metadata: %+v", submission)
		}
	}
	var hostSubmissionID, bobSubmissionID string
	for _, submission := range voting.Today.Submissions {
		if submission.Mine {
			hostSubmissionID = submission.ID
		} else {
			bobSubmissionID = submission.ID
		}
	}
	if err := service.Vote(ctx, roundID, host.Token, hostSubmissionID); !errors.Is(err, ErrOwnSubmission) {
		t.Fatalf("self vote error = %v", err)
	}
	if err := service.Vote(ctx, roundID, host.Token, bobSubmissionID); err != nil {
		t.Fatal(err)
	}
	if err := service.Vote(ctx, roundID, bob.Token, hostSubmissionID); err != nil {
		t.Fatal(err)
	}
	if err := service.Vote(ctx, roundID, bob.Token, hostSubmissionID); err != nil {
		t.Fatalf("idempotent vote: %v", err)
	}

	now = time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)
	var wait sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, tickErr := service.Tick(ctx)
			errCh <- tickErr
		}()
	}
	wait.Wait()
	close(errCh)
	for tickErr := range errCh {
		if tickErr != nil {
			t.Fatal(tickErr)
		}
	}
	finalView, err := service.Today(ctx, host.Group.Code, host.Token)
	if err != nil {
		t.Fatal(err)
	}
	if finalView.Today.Number != 2 || finalView.Previous == nil || finalView.Previous.State != StateFinalized {
		t.Fatalf("next/finalized rounds = %+v", finalView)
	}
	if finalView.Streak != 1 {
		t.Fatalf("streak = %d, want 1", finalView.Streak)
	}
	var roundCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM daily_rounds WHERE group_id = $1::uuid`, host.Group.ID).Scan(&roundCount); err != nil {
		t.Fatal(err)
	}
	if roundCount != 2 {
		t.Fatalf("round count after concurrent ticks = %d, want 2", roundCount)
	}
	if len(finalView.Previous.Submissions) != 2 {
		t.Fatalf("final submissions = %+v", finalView.Previous.Submissions)
	}
	for _, submission := range finalView.Previous.Submissions {
		if submission.AuthorName == "" || !submission.Winner || submission.VoteCount != 1 {
			t.Fatalf("finalized tie metadata = %+v", submission)
		}
	}
	if err := service.DeleteGroup(ctx, host.Group.Code, bob.Token); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("member delete error = %v", err)
	}
	if err := service.DeleteGroup(ctx, host.Group.Code, host.Token); err != nil {
		t.Fatal(err)
	}
	deleted = true
}

func TestNormalizeCandidateTextCollapsesVariants(t *testing.T) {
	same := []string{"A spreadsheet, with feelings!", "a  spreadsheet with feelings", "A SPREADSHEET WITH FEELINGS..."}
	first := NormalizeCandidateText(same[0])
	for _, variant := range same[1:] {
		if got := NormalizeCandidateText(variant); got != first {
			t.Fatalf("normalize(%q) = %q, want %q", variant, got, first)
		}
	}
	if NormalizeCandidateText("!!!") != "" {
		t.Fatal("punctuation-only text should normalize to empty")
	}
}

// Finalizing a round harvests the answers the group voted for. This is the
// content pipeline: without it there is no source of new cards.
func TestPostgresFinalizationHarvestsCandidates(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	service := NewService(db, cards.NewSeedDeck())
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	host, err := service.CreateGroup(ctx, CreateGroupInput{Name: "Harvest Crew", PlayerName: "Host", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DELETE FROM daily_groups WHERE id = $1::uuid`, host.Group.ID)

	members := []Session{host}
	for _, name := range []string{"Bea", "Cy", "Dev"} {
		member, err := service.JoinGroup(ctx, host.Group.Code, JoinGroupInput{PlayerName: name})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, member)
	}

	view, err := service.Today(ctx, host.Group.Code, host.Token)
	if err != nil {
		t.Fatal(err)
	}
	roundID := view.Today.ID
	winningText := fmt.Sprintf("The winning line %d", now.UnixNano())
	answers := []string{winningText, "second place line", "third place line", "fourth place line"}
	for i, member := range members {
		if err := service.Submit(ctx, roundID, member.Token, answers[i]); err != nil {
			t.Fatal(err)
		}
	}

	now = time.Date(2026, 9, 3, 19, 0, 0, 0, time.UTC)
	if _, err := service.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	voting, err := service.Today(ctx, host.Group.Code, host.Token)
	if err != nil {
		t.Fatal(err)
	}
	var hostSubmissionID string
	for _, submission := range voting.Today.Submissions {
		if submission.Mine {
			hostSubmissionID = submission.ID
		}
	}
	// Three of the four vote for the host's answer; the host votes elsewhere.
	for _, member := range members[1:] {
		if err := service.Vote(ctx, roundID, member.Token, hostSubmissionID); err != nil {
			t.Fatal(err)
		}
	}

	now = time.Date(2026, 9, 4, 14, 0, 0, 0, time.UTC)
	if _, err := service.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	var candidateText string
	var voteCount int
	var status string
	err = db.QueryRowContext(ctx, `
		SELECT text, vote_count, status::text FROM card_candidates
		WHERE normalized_text = $1`, NormalizeCandidateText(winningText)).Scan(&candidateText, &voteCount, &status)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("the winning answer did not become a candidate")
	}
	if err != nil {
		t.Fatal(err)
	}
	if candidateText != winningText || voteCount != 3 || status != "pending" {
		t.Fatalf("candidate = %q/%d/%s", candidateText, voteCount, status)
	}

	// Answers nobody voted for must not enter the queue.
	var unvoted int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM card_candidates WHERE normalized_text = $1`,
		NormalizeCandidateText("fourth place line")).Scan(&unvoted); err != nil {
		t.Fatal(err)
	}
	if unvoted != 0 {
		t.Fatal("an answer with no votes became a candidate")
	}

	// Deleting the group withdraws its un-promoted candidates.
	if err := service.DeleteGroup(ctx, host.Group.Code, host.Token); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM card_candidates WHERE normalized_text = $1`,
		NormalizeCandidateText(winningText)).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("deleting a group left its pending candidates behind")
	}
}

// Only one instance should scan for due transitions. Holding the advisory lock
// elsewhere must make Tick a no-op rather than an error or a duplicate scan.
func TestPostgresTickYieldsWhenAnotherInstanceHoldsTheLock(t *testing.T) {
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

	service := NewService(db, cards.NewSeedDeck())
	host, err := service.CreateGroup(ctx, CreateGroupInput{Name: "Lock Crew", PlayerName: "Host", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `DELETE FROM daily_groups WHERE id = $1::uuid`, host.Group.ID)

	// Stand in for another instance mid-tick.
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	var acquired bool
	if err := holder.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, tickAdvisoryLock).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("could not acquire the tick lock for the test")
	}

	result, err := service.Tick(ctx)
	if err != nil {
		t.Fatalf("a yielding tick must not error: %v", err)
	}
	if result != (TickResult{}) {
		t.Fatalf("a yielding tick did work anyway: %+v", result)
	}

	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, tickAdvisoryLock); err != nil {
		t.Fatal(err)
	}
	// With the lock free the next tick proceeds normally.
	if _, err := service.Tick(ctx); err != nil {
		t.Fatalf("tick after release: %v", err)
	}
}
