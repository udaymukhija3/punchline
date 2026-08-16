package daily

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"

	"punchline/backend/internal/cards"
	"punchline/backend/internal/telemetry"
)

const groupCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

const (
	// defaultRevealHour is the local hour a new group reveals at until its
	// owner changes it.
	defaultRevealHour = 18
	// votingWindow is how long voting stays open after the reveal.
	votingWindow = 18 * time.Hour
)

type Service struct {
	db *sql.DB
	// promptsMu guards prompts alone: the deck is refreshed in the background
	// while rounds are being created.
	promptsMu sync.RWMutex
	prompts   []cards.PromptCard
	now       func() time.Time
	telemetry telemetry.Recorder
}

// SetDeck refreshes the prompt pool daily rounds draw from, so a retired or
// promoted card reaches tomorrow's prompt without a redeploy. Today's rounds
// are already written and keep the prompt they were created with.
func (s *Service) SetDeck(deck cards.Deck) bool {
	if s == nil {
		return false
	}
	prompts := deck.For(cards.TierFamily).Prompts
	if len(prompts) == 0 {
		return false
	}
	s.promptsMu.Lock()
	defer s.promptsMu.Unlock()
	if len(prompts) == len(s.prompts) {
		same := true
		for i := range prompts {
			if prompts[i].ID != s.prompts[i].ID || prompts[i].Text != s.prompts[i].Text {
				same = false
				break
			}
		}
		if same {
			return false
		}
	}
	s.prompts = prompts
	return true
}

func (s *Service) promptPool() []cards.PromptCard {
	s.promptsMu.RLock()
	defer s.promptsMu.RUnlock()
	return s.prompts
}

// SetCardTelemetry attaches a card telemetry recorder. A daily round "plays"
// its prompt once, counted when the round is durably created.
func (s *Service) SetCardTelemetry(recorder telemetry.Recorder) {
	if s != nil {
		s.telemetry = recorder
	}
}

func NewService(db *sql.DB, deck cards.Deck) *Service {
	prompts := deck.For(cards.TierFamily).Prompts
	return &Service{db: db, prompts: prompts, now: time.Now}
}

func UnavailableService() *Service {
	return &Service{now: time.Now}
}

func (s *Service) Available() bool { return s != nil && s.db != nil && len(s.promptPool()) > 0 }

func (s *Service) Ready(ctx context.Context) error {
	if !s.Available() {
		return nil
	}
	var ok int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM daily_groups LIMIT 1`).Scan(&ok); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("daily store readiness: %w", err)
	}
	return nil
}

func (s *Service) CreateGroup(ctx context.Context, input CreateGroupInput) (Session, error) {
	if !s.Available() {
		return Session{}, ErrUnavailable
	}
	groupName, playerName, location, err := validateCreateInput(input)
	if err != nil {
		return Session{}, err
	}
	groupID, err := randomUUID()
	if err != nil {
		return Session{}, err
	}
	membershipID, err := randomUUID()
	if err != nil {
		return Session{}, err
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return Session{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin daily group creation: %w", err)
	}
	defer tx.Rollback()

	code := ""
	for attempt := 0; attempt < 12; attempt++ {
		candidate, genErr := randomCode(6)
		if genErr != nil {
			return Session{}, genErr
		}
		var inserted string
		err = tx.QueryRowContext(ctx, `
			INSERT INTO daily_groups (id, code, name, timezone)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (code) DO NOTHING
			RETURNING code`, groupID, candidate, groupName, location.String()).Scan(&inserted)
		if err == nil {
			code = inserted
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Session{}, fmt.Errorf("insert daily group: %w", err)
		}
	}
	if code == "" {
		return Session{}, errors.New("could not allocate a daily group code")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO daily_memberships (id, group_id, display_name, token_hash, role)
		VALUES ($1::uuid, $2::uuid, $3, $4, 'owner')`, membershipID, groupID, playerName, tokenHash); err != nil {
		return Session{}, fmt.Errorf("insert daily owner: %w", err)
	}
	_, firstPrompt, err := s.ensureRoundTx(ctx, tx, groupID, location.String(), defaultRevealHour, s.now().UTC())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit daily group: %w", err)
	}
	if s.telemetry != nil {
		s.telemetry.PromptPlayed(firstPrompt.UUID)
	}
	return Session{
		Group:      Group{ID: groupID, Code: code, Name: groupName, Timezone: location.String(), RevealHour: defaultRevealHour, MemberCount: 1},
		Membership: Membership{ID: membershipID, DisplayName: playerName, Role: "owner"},
		Token:      token,
	}, nil
}

func (s *Service) JoinGroup(ctx context.Context, code string, input JoinGroupInput) (Session, error) {
	if !s.Available() {
		return Session{}, ErrUnavailable
	}
	code = normalizeCode(code)
	if len(code) != 6 {
		return Session{}, badInput("That group code doesn't look right — they're six characters")
	}
	name, err := validateName(input.PlayerName, 1, 20, "Your name")
	if err != nil {
		return Session{}, err
	}
	membershipID, err := randomUUID()
	if err != nil {
		return Session{}, err
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, fmt.Errorf("begin daily join: %w", err)
	}
	defer tx.Rollback()

	var group Group
	var maxMembers int
	err = tx.QueryRowContext(ctx, `
		SELECT id::text, code, name, timezone, reveal_hour, max_members,
		       (SELECT count(*) FROM daily_memberships m WHERE m.group_id = g.id)
		FROM daily_groups g WHERE code = $1 FOR UPDATE`, code).Scan(
		&group.ID, &group.Code, &group.Name, &group.Timezone, &group.RevealHour, &maxMembers, &group.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("load daily group: %w", err)
	}
	if group.MemberCount >= maxMembers {
		return Session{}, ErrGroupFull
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO daily_memberships (id, group_id, display_name, token_hash)
		VALUES ($1::uuid, $2::uuid, $3, $4)`, membershipID, group.ID, name, tokenHash)
	if err != nil {
		if sqlState(err) == "23505" {
			return Session{}, ErrConflict
		}
		return Session{}, fmt.Errorf("insert daily membership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit daily join: %w", err)
	}
	group.MemberCount++
	return Session{Group: group, Membership: Membership{ID: membershipID, DisplayName: name, Role: "member"}, Token: token}, nil
}

func (s *Service) DeleteGroup(ctx context.Context, code, token string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	code = normalizeCode(code)
	if token == "" || len(code) != 6 {
		return ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM daily_groups g
		WHERE g.code = $1
		  AND EXISTS (
		      SELECT 1 FROM daily_memberships m
		      WHERE m.group_id = g.id AND m.token_hash = $2 AND m.role = 'owner'
		  )`, code, hash[:])
	if err != nil {
		return fmt.Errorf("delete daily group: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete daily group rows: %w", err)
	}
	if affected == 0 {
		return ErrUnauthorized
	}
	return nil
}

// UpdateGroup lets the owner move the group's daily deadline. Changing the
// reveal hour or timezone reshapes today's round if it is still open; rounds
// that have already revealed are left alone, because players have seen them.
func (s *Service) UpdateGroup(ctx context.Context, code, token string, input UpdateGroupInput) (Group, error) {
	if !s.Available() {
		return Group{}, ErrUnavailable
	}
	member, group, err := s.authorizeGroup(ctx, normalizeCode(code), token)
	if err != nil {
		return Group{}, err
	}
	if member.Role != "owner" {
		return Group{}, ErrUnauthorized
	}

	revealHour := group.RevealHour
	if input.RevealHour != nil {
		revealHour = *input.RevealHour
		if revealHour < 0 || revealHour > 23 {
			return Group{}, ErrInvalidInput
		}
	}
	timezone := group.Timezone
	if input.Timezone != nil {
		candidate := strings.TrimSpace(*input.Timezone)
		if candidate == "" {
			return Group{}, ErrInvalidInput
		}
		loc, err := time.LoadLocation(candidate)
		if err != nil {
			return Group{}, ErrInvalidInput
		}
		timezone = loc.String()
	}
	name := group.Name
	if input.Name != nil {
		validated, err := validateName(*input.Name, 2, 40, "The group name")
		if err != nil {
			return Group{}, err
		}
		name = validated
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, fmt.Errorf("begin daily group update: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE daily_groups SET name = $2, timezone = $3, reveal_hour = $4, updated_at = now()
		WHERE id = $1::uuid`, group.ID, name, timezone, revealHour); err != nil {
		return Group{}, fmt.Errorf("update daily group: %w", err)
	}

	// Re-time today's round so the change takes effect immediately rather than
	// tomorrow. Only while submissions are still open.
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return Group{}, fmt.Errorf("load updated timezone: %w", err)
	}
	localNow := s.now().UTC().In(loc)
	revealAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), revealHour, 0, 0, 0, loc)
	opensAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	if !revealAt.After(opensAt) {
		revealAt = opensAt.Add(time.Hour)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE daily_rounds
		SET reveal_at = $2, voting_closes_at = $3, updated_at = now()
		WHERE group_id = $1::uuid AND state = 'OPEN_FOR_SUBMISSIONS' AND round_date = $4::date`,
		group.ID, revealAt.UTC(), revealAt.Add(votingWindow).UTC(), localNow.Format("2006-01-02")); err != nil {
		return Group{}, fmt.Errorf("retime open daily round: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Group{}, fmt.Errorf("commit daily group update: %w", err)
	}

	group.Name, group.Timezone, group.RevealHour = name, timezone, revealHour
	return group, nil
}

func (s *Service) Today(ctx context.Context, code, token string) (TodayView, error) {
	if !s.Available() {
		return TodayView{}, ErrUnavailable
	}
	member, group, err := s.authorizeGroup(ctx, normalizeCode(code), token)
	if err != nil {
		return TodayView{}, err
	}
	now := s.now().UTC()
	if _, err := s.ensureRound(ctx, group.ID, group.Timezone, group.RevealHour, now); err != nil {
		return TodayView{}, err
	}
	if _, _, err := s.advanceDue(ctx, group.ID, now); err != nil {
		return TodayView{}, err
	}

	loc, _ := time.LoadLocation(group.Timezone)
	localDate := now.In(loc).Format("2006-01-02")
	var todayID string
	err = s.db.QueryRowContext(ctx, `SELECT id::text FROM daily_rounds WHERE group_id = $1::uuid AND round_date = $2::date`, group.ID, localDate).Scan(&todayID)
	if err != nil {
		return TodayView{}, fmt.Errorf("load current daily round: %w", err)
	}
	today, err := s.loadRound(ctx, todayID, member.ID, group.MemberCount)
	if err != nil {
		return TodayView{}, err
	}

	var previous *Round
	var previousID string
	err = s.db.QueryRowContext(ctx, `
		SELECT id::text FROM daily_rounds
		WHERE group_id = $1::uuid AND round_date < $2::date
		ORDER BY round_date DESC LIMIT 1`, group.ID, localDate).Scan(&previousID)
	if err == nil {
		round, loadErr := s.loadRound(ctx, previousID, member.ID, group.MemberCount)
		if loadErr != nil {
			return TodayView{}, loadErr
		}
		previous = &round
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TodayView{}, fmt.Errorf("load previous daily round: %w", err)
	}
	streak, err := s.groupStreak(ctx, group.ID, now, loc)
	if err != nil {
		return TodayView{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE daily_memberships SET last_seen_at = now() WHERE id = $1::uuid`, member.ID)
	return TodayView{Group: group, Membership: member, Today: today, Previous: previous, Streak: streak, ServerTime: now}, nil
}

func (s *Service) Submit(ctx context.Context, roundID, token, answer string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	answer = strings.TrimSpace(answer)
	switch {
	case !validUUID(roundID):
		return badInput("That round link is no longer valid — reload the page")
	case answer == "":
		return badInput("Write something before locking it in")
	case utf8.RuneCountInString(answer) > 160:
		return badInput("Punchlines have to be 160 characters or fewer")
	case hasUnsafeControl(answer):
		return badInput("Your punchline can only use ordinary characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin daily submit: %w", err)
	}
	defer tx.Rollback()
	memberID, groupID, state, revealAt, _, err := authorizeRoundTx(ctx, tx, roundID, token)
	if err != nil {
		return err
	}
	if state != StateOpen || !s.now().UTC().Before(revealAt) {
		return ErrRoundClosed
	}
	submissionID, err := randomUUID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO daily_submissions (id, group_id, daily_round_id, membership_id, answer_text)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)
		ON CONFLICT (daily_round_id, membership_id) DO UPDATE
		SET answer_text = EXCLUDED.answer_text, updated_at = now()`, submissionID, groupID, roundID, memberID, answer)
	if err != nil {
		return fmt.Errorf("save daily submission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit daily submission: %w", err)
	}
	return nil
}

func (s *Service) Vote(ctx context.Context, roundID, token, submissionID string) error {
	if !s.Available() {
		return ErrUnavailable
	}
	if !validUUID(roundID) || !validUUID(submissionID) {
		return badInput("That vote didn't land — reload the page and try again")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin daily vote: %w", err)
	}
	defer tx.Rollback()
	memberID, groupID, state, _, votingClosesAt, err := authorizeRoundTx(ctx, tx, roundID, token)
	if err != nil {
		return err
	}
	if state != StateVoting || !s.now().UTC().Before(votingClosesAt) {
		return ErrVotingClosed
	}
	var authorID string
	err = tx.QueryRowContext(ctx, `
		SELECT membership_id::text FROM daily_submissions
		WHERE id = $1::uuid AND daily_round_id = $2::uuid`, submissionID, roundID).Scan(&authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load daily vote target: %w", err)
	}
	if authorID == memberID {
		return ErrOwnSubmission
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO daily_votes (group_id, daily_round_id, voter_membership_id, submission_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid)
		ON CONFLICT (daily_round_id, voter_membership_id) DO UPDATE
		SET submission_id = EXCLUDED.submission_id, updated_at = now()`, groupID, roundID, memberID, submissionID)
	if err != nil {
		return fmt.Errorf("save daily vote: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit daily vote: %w", err)
	}
	return nil
}

// tickAdvisoryLock is the Postgres advisory lock id that keeps daily
// transitions to one instance at a time. The transitions are already
// idempotent, so this is about not doing the same scan on every machine (and
// not counting it three times in metrics), not about correctness.
const tickAdvisoryLock = 0x50554e43 // "PUNC"

func (s *Service) Tick(ctx context.Context) (TickResult, error) {
	if !s.Available() {
		return TickResult{}, ErrUnavailable
	}

	// Try to become the ticking instance. Whoever loses simply skips this
	// round; the winner's work covers every group.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return TickResult{}, fmt.Errorf("acquire daily tick connection: %w", err)
	}
	defer conn.Close()
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, tickAdvisoryLock).Scan(&acquired); err != nil {
		return TickResult{}, fmt.Errorf("acquire daily tick lock: %w", err)
	}
	if !acquired {
		return TickResult{}, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, tickAdvisoryLock)
	}()

	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, timezone, reveal_hour FROM daily_groups ORDER BY id`)
	if err != nil {
		return TickResult{}, fmt.Errorf("list daily groups: %w", err)
	}
	type groupConfig struct {
		id, timezone string
		revealHour   int
	}
	groups := []groupConfig{}
	for rows.Next() {
		var group groupConfig
		if err := rows.Scan(&group.id, &group.timezone, &group.revealHour); err != nil {
			rows.Close()
			return TickResult{}, fmt.Errorf("scan daily group: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Close(); err != nil {
		return TickResult{}, err
	}
	result := TickResult{}
	for _, group := range groups {
		created, err := s.ensureRound(ctx, group.id, group.timezone, group.revealHour, now)
		if err != nil {
			return result, err
		}
		if created {
			result.RoundsCreated++
		}
	}
	revealed, finalized, err := s.advanceDue(ctx, "", now)
	if err != nil {
		return result, err
	}
	result.RoundsRevealed = revealed
	result.RoundsFinalized = finalized
	return result, nil
}

func (s *Service) StartWorker(ctx context.Context, interval time.Duration, observe func(TickResult, error)) {
	if !s.Available() {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			result, err := s.Tick(ctx)
			if observe != nil {
				observe(result, err)
			}
			if err != nil {
				log.Printf("daily worker: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) ensureRound(ctx context.Context, groupID, timezone string, revealHour int, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin daily round ensure: %w", err)
	}
	defer tx.Rollback()
	created, prompt, err := s.ensureRoundTx(ctx, tx, groupID, timezone, revealHour, now)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit daily round ensure: %w", err)
	}
	// Counted only after commit, so a rolled-back round never inflates a card.
	if created && s.telemetry != nil {
		s.telemetry.PromptPlayed(prompt.UUID)
	}
	return created, nil
}

func (s *Service) ensureRoundTx(ctx context.Context, tx *sql.Tx, groupID, timezone string, revealHour int, now time.Time) (bool, cards.PromptCard, error) {
	if _, err := tx.ExecContext(ctx, `SELECT id FROM daily_groups WHERE id = $1::uuid FOR UPDATE`, groupID); err != nil {
		return false, cards.PromptCard{}, fmt.Errorf("lock daily group: %w", err)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return false, cards.PromptCard{}, fmt.Errorf("load daily group timezone: %w", err)
	}
	localNow := now.In(loc)
	date := localNow.Format("2006-01-02")
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM daily_rounds WHERE group_id = $1::uuid AND round_date = $2::date)`, groupID, date).Scan(&exists); err != nil {
		return false, cards.PromptCard{}, fmt.Errorf("check daily round: %w", err)
	}
	if exists {
		return false, cards.PromptCard{}, nil
	}
	var number int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(round_number), 0) + 1 FROM daily_rounds WHERE group_id = $1::uuid`, groupID).Scan(&number); err != nil {
		return false, cards.PromptCard{}, fmt.Errorf("number daily round: %w", err)
	}
	roundID, err := randomUUID()
	if err != nil {
		return false, cards.PromptCard{}, err
	}
	prompt := s.promptFor(groupID, date)
	opensAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	revealAt := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), revealHour, 0, 0, 0, loc)
	if !revealAt.After(opensAt) {
		revealAt = opensAt.Add(time.Hour)
	}
	votingClosesAt := revealAt.Add(votingWindow)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO daily_rounds
		(id, group_id, round_number, round_date, prompt_key, prompt_text, opens_at, reveal_at, voting_closes_at)
		VALUES ($1::uuid, $2::uuid, $3, $4::date, $5, $6, $7, $8, $9)`,
		roundID, groupID, number, date, prompt.ID, prompt.Text, opensAt.UTC(), revealAt.UTC(), votingClosesAt.UTC())
	if err != nil {
		return false, cards.PromptCard{}, fmt.Errorf("insert daily round: %w", err)
	}
	return true, prompt, nil
}

func (s *Service) advanceDue(ctx context.Context, groupID string, now time.Time) (int, int, error) {
	groupClause := ""
	args := []any{now}
	if groupID != "" {
		groupClause = " AND group_id = $2::uuid"
		args = append(args, groupID)
	}
	revealResult, err := s.db.ExecContext(ctx, `
		UPDATE daily_rounds SET state = 'REVEALED_FOR_VOTING', updated_at = now()
		WHERE state = 'OPEN_FOR_SUBMISSIONS' AND reveal_at <= $1`+groupClause, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("reveal due daily rounds: %w", err)
	}
	finalized, err := s.finalizeRounds(ctx, groupClause, args)
	if err != nil {
		return 0, 0, err
	}
	revealed, _ := revealResult.RowsAffected()
	return int(revealed), finalized, nil
}

func (s *Service) authorizeGroup(ctx context.Context, code, token string) (Membership, Group, error) {
	if token == "" || len(code) != 6 {
		return Membership{}, Group{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	var member Membership
	var group Group
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id::text, m.display_name, m.role,
		       g.id::text, g.code, g.name, g.timezone, g.reveal_hour,
		       (SELECT count(*) FROM daily_memberships m2 WHERE m2.group_id = g.id)
		FROM daily_memberships m
		JOIN daily_groups g ON g.id = m.group_id
		WHERE g.code = $1 AND m.token_hash = $2`, code, hash[:]).Scan(
		&member.ID, &member.DisplayName, &member.Role,
		&group.ID, &group.Code, &group.Name, &group.Timezone, &group.RevealHour, &group.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Membership{}, Group{}, ErrUnauthorized
	}
	if err != nil {
		return Membership{}, Group{}, fmt.Errorf("authorize daily membership: %w", err)
	}
	return member, group, nil
}

func authorizeRoundTx(ctx context.Context, tx *sql.Tx, roundID, token string) (memberID, groupID, state string, revealAt, votingClosesAt time.Time, err error) {
	if token == "" || strings.TrimSpace(roundID) == "" {
		err = ErrUnauthorized
		return
	}
	hash := sha256.Sum256([]byte(token))
	err = tx.QueryRowContext(ctx, `
		SELECT m.id::text, r.group_id::text, r.state::text, r.reveal_at, r.voting_closes_at
		FROM daily_rounds r
		JOIN daily_memberships m ON m.group_id = r.group_id
		WHERE r.id = $1::uuid AND m.token_hash = $2
		FOR UPDATE OF r`, roundID, hash[:]).Scan(&memberID, &groupID, &state, &revealAt, &votingClosesAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnauthorized
	} else if err != nil {
		err = fmt.Errorf("authorize daily round: %w", err)
	}
	return
}

func (s *Service) loadRound(ctx context.Context, roundID, memberID string, memberCount int) (Round, error) {
	var round Round
	err := s.db.QueryRowContext(ctx, `
		SELECT id::text, round_number, round_date::text, prompt_text, state::text,
		       opens_at, reveal_at, voting_closes_at,
		       (SELECT count(*) FROM daily_submissions WHERE daily_round_id = r.id)
		FROM daily_rounds r WHERE id = $1::uuid`, roundID).Scan(
		&round.ID, &round.Number, &round.Date, &round.Prompt, &round.State,
		&round.OpensAt, &round.RevealAt, &round.VotingClosesAt, &round.SubmissionCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Round{}, ErrNotFound
	}
	if err != nil {
		return Round{}, fmt.Errorf("load daily round: %w", err)
	}
	round.MemberCount = memberCount

	var mine Submission
	err = s.db.QueryRowContext(ctx, `
		SELECT id::text, answer_text, submitted_at
		FROM daily_submissions WHERE daily_round_id = $1::uuid AND membership_id = $2::uuid`, roundID, memberID).Scan(
		&mine.ID, &mine.AnswerText, &mine.SubmittedAt)
	if err == nil {
		mine.Mine = true
		round.MySubmission = &mine
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Round{}, fmt.Errorf("load own daily submission: %w", err)
	}
	if round.State == StateOpen {
		return round, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id::text, s.answer_text, m.display_name, s.membership_id::text,
		       count(v.submission_id)::int, s.submitted_at,
		       EXISTS(SELECT 1 FROM daily_votes mine WHERE mine.daily_round_id = s.daily_round_id
		              AND mine.voter_membership_id = $2::uuid AND mine.submission_id = s.id)
		FROM daily_submissions s
		JOIN daily_memberships m ON m.id = s.membership_id
		LEFT JOIN daily_votes v ON v.submission_id = s.id
		WHERE s.daily_round_id = $1::uuid
		GROUP BY s.id, m.display_name
		ORDER BY s.submitted_at, s.id`, roundID, memberID)
	if err != nil {
		return Round{}, fmt.Errorf("list daily submissions: %w", err)
	}
	type loadedSubmission struct {
		item     Submission
		authorID string
	}
	loaded := []loadedSubmission{}
	maxVotes := -1
	for rows.Next() {
		var entry loadedSubmission
		if err := rows.Scan(&entry.item.ID, &entry.item.AnswerText, &entry.item.AuthorName, &entry.authorID,
			&entry.item.VoteCount, &entry.item.SubmittedAt, &entry.item.MyVote); err != nil {
			rows.Close()
			return Round{}, fmt.Errorf("scan daily submission: %w", err)
		}
		entry.item.Mine = entry.authorID == memberID
		if entry.item.MyVote {
			round.MyVoteSubmissionID = entry.item.ID
		}
		if entry.item.VoteCount > maxVotes {
			maxVotes = entry.item.VoteCount
		}
		loaded = append(loaded, entry)
	}
	if err := rows.Close(); err != nil {
		return Round{}, err
	}
	for _, entry := range loaded {
		item := entry.item
		if round.State != StateFinalized {
			item.AuthorName = ""
			item.VoteCount = 0
		} else if maxVotes > 0 && item.VoteCount == maxVotes {
			item.Winner = true
		}
		round.Submissions = append(round.Submissions, item)
	}
	return round, nil
}

func (s *Service) groupStreak(ctx context.Context, groupID string, now time.Time, loc *time.Location) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.round_date::text
		FROM daily_rounds r
		WHERE r.group_id = $1::uuid AND r.state = 'FINALIZED'
		  AND (SELECT count(*) FROM daily_submissions s WHERE s.daily_round_id = r.id) >= 2
		ORDER BY r.round_date DESC LIMIT 366`, groupID)
	if err != nil {
		return 0, fmt.Errorf("load daily streak: %w", err)
	}
	dates := []time.Time{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return 0, err
		}
		date, err := time.ParseInLocation("2006-01-02", value, loc)
		if err != nil {
			rows.Close()
			return 0, err
		}
		dates = append(dates, date)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(dates) == 0 {
		return 0, nil
	}
	today := now.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	if dates[0].Before(today.AddDate(0, 0, -1)) {
		return 0, nil
	}
	streak := 1
	for i := 1; i < len(dates); i++ {
		if dates[i].Equal(dates[i-1].AddDate(0, 0, -1)) {
			streak++
		} else {
			break
		}
	}
	return streak, nil
}

func (s *Service) promptFor(groupID, date string) cards.PromptCard {
	prompts := s.promptPool()
	digest := sha256.Sum256([]byte(groupID + ":" + date))
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(prompts))
	return prompts[index]
}

func validateCreateInput(input CreateGroupInput) (string, string, *time.Location, error) {
	groupName, err := validateName(input.Name, 2, 40, "The group name")
	if err != nil {
		return "", "", nil, err
	}
	playerName, err := validateName(input.PlayerName, 1, 20, "Your name")
	if err != nil {
		return "", "", nil, err
	}
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return "", "", nil, badInput("We couldn't read your timezone — check your device's clock settings")
	}
	return groupName, playerName, location, nil
}

func validateName(value string, min, max int, label string) (string, error) {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if hasUnsafeControl(value) {
		return "", badInput(label + " can only use ordinary characters")
	}
	count := utf8.RuneCountInString(value)
	if count < min {
		if min == 1 {
			return "", badInput(label + " can't be empty")
		}
		return "", badInput(fmt.Sprintf("%s needs at least %d characters", label, min))
	}
	if count > max {
		return "", badInput(fmt.Sprintf("%s has to be %d characters or fewer", label, max))
	}
	return value, nil
}

func hasUnsafeControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return true
		}
	}
	return false
}

func normalizeCode(value string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(value) {
		if strings.ContainsRune(groupCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
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

func randomCode(length int) (string, error) {
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for i := range raw {
		raw[i] = groupCodeAlphabet[int(raw[i])%len(groupCodeAlphabet)]
	}
	return string(raw), nil
}

func newToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func randomUUID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func sqlState(err error) string {
	type sqlStater interface{ SQLState() string }
	var target sqlStater
	if errors.As(err, &target) {
		return target.SQLState()
	}
	return ""
}
