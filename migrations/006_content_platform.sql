-- Content platform: player reporting, and a card pipeline fed by daily winners
-- rather than generated content.

CREATE TYPE card_report_resolution AS ENUM ('retired', 'dismissed');
CREATE TYPE card_candidate_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE card_reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prompt_card_id UUID REFERENCES prompt_cards(id) ON DELETE CASCADE,
    answer_card_id UUID REFERENCES answer_cards(id) ON DELETE CASCADE,
    reason TEXT NOT NULL CHECK (reason IN ('offensive', 'broken', 'unfunny', 'duplicate', 'other')),
    detail TEXT NOT NULL DEFAULT '' CHECK (char_length(detail) <= 280),
    -- Salted hash of the reporting client. Enough to stop one person filing the
    -- same report repeatedly, without storing anything identifying.
    reporter_hash BYTEA NOT NULL CHECK (octet_length(reporter_hash) = 32),
    room_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    resolution card_report_resolution,
    CHECK (num_nonnulls(prompt_card_id, answer_card_id) = 1),
    CHECK ((resolution IS NULL AND resolved_at IS NULL) OR (resolution IS NOT NULL AND resolved_at IS NOT NULL))
);

-- One report per card per reporter; repeats update the existing row.
CREATE UNIQUE INDEX idx_card_reports_unique_reporter
    ON card_reports ((COALESCE(prompt_card_id, answer_card_id)), reporter_hash);
CREATE INDEX idx_card_reports_open ON card_reports(created_at DESC) WHERE resolved_at IS NULL;

-- Candidate cards harvested from daily rounds: answers that real players wrote
-- and real players voted for. An admin promotes them into the community pack.
CREATE TABLE card_candidates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    text TEXT NOT NULL CHECK (char_length(text) BETWEEN 1 AND 160),
    -- Lowercased, punctuation-stripped form used only for dedupe.
    normalized_text TEXT NOT NULL UNIQUE,
    tier content_tier NOT NULL DEFAULT 'party',
    author_name TEXT NOT NULL DEFAULT '',
    vote_count INT NOT NULL DEFAULT 0 CHECK (vote_count >= 0),
    -- Candidates are withdrawn if the group that produced them is deleted, so
    -- deleting a group also withdraws its un-promoted content.
    source_round_id UUID REFERENCES daily_rounds(id) ON DELETE CASCADE,
    status card_candidate_status NOT NULL DEFAULT 'pending',
    promoted_answer_card_id UUID REFERENCES answer_cards(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ,
    CHECK ((status = 'pending') = (reviewed_at IS NULL))
);

CREATE INDEX idx_card_candidates_pending
    ON card_candidates(vote_count DESC, created_at) WHERE status = 'pending';

-- Home for promoted player content. Separate from the official pack so a room
-- could later opt out of community cards without losing the launch deck.
INSERT INTO packs (slug, title, description, source, tier, status)
VALUES (
    'community-daily',
    'From the Daily',
    'Answers players wrote and voted for in daily rounds.',
    'community',
    'party',
    'approved'
)
ON CONFLICT (slug) DO NOTHING;
