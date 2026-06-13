CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE content_tier AS ENUM ('family', 'party', 'unfiltered');
CREATE TYPE card_source AS ENUM ('official', 'ai', 'community', 'topical');
CREATE TYPE card_status AS ENUM ('draft', 'approved', 'rejected', 'retired');
CREATE TYPE room_phase AS ENUM ('lobby', 'submitting', 'judging', 'scoring', 'finished');

CREATE TABLE packs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug TEXT UNIQUE NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source card_source NOT NULL DEFAULT 'official',
    tier content_tier NOT NULL DEFAULT 'party',
    status card_status NOT NULL DEFAULT 'approved',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE prompt_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pack_id UUID NOT NULL REFERENCES packs(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    blanks INT NOT NULL DEFAULT 1,
    source card_source NOT NULL DEFAULT 'official',
    tier content_tier NOT NULL DEFAULT 'party',
    status card_status NOT NULL DEFAULT 'approved',
    times_played INT NOT NULL DEFAULT 0,
    skip_count INT NOT NULL DEFAULT 0,
    report_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE answer_cards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pack_id UUID NOT NULL REFERENCES packs(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    source card_source NOT NULL DEFAULT 'official',
    tier content_tier NOT NULL DEFAULT 'party',
    status card_status NOT NULL DEFAULT 'approved',
    times_played INT NOT NULL DEFAULT 0,
    win_count INT NOT NULL DEFAULT 0,
    report_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE rooms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT UNIQUE NOT NULL,
    phase room_phase NOT NULL DEFAULT 'lobby',
    content_tier content_tier NOT NULL DEFAULT 'party',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE players (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    score INT NOT NULL DEFAULT 0,
    guest_session_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE rounds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    round_number INT NOT NULL,
    prompt_card_id UUID REFERENCES prompt_cards(id),
    judge_player_id UUID REFERENCES players(id),
    phase room_phase NOT NULL DEFAULT 'submitting',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at TIMESTAMPTZ
);

CREATE TABLE submissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    round_id UUID NOT NULL REFERENCES rounds(id) ON DELETE CASCADE,
    player_id UUID NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    answer_card_id UUID REFERENCES answer_cards(id),
    is_winner BOOLEAN NOT NULL DEFAULT false,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(round_id, player_id)
);

CREATE INDEX idx_rooms_code ON rooms(code);
CREATE INDEX idx_prompt_cards_pack_status ON prompt_cards(pack_id, status);
CREATE INDEX idx_answer_cards_pack_status ON answer_cards(pack_id, status);
CREATE INDEX idx_rounds_room_number ON rounds(room_id, round_number);
CREATE INDEX idx_submissions_round ON submissions(round_id);
