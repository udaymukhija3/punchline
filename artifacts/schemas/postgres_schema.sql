-- PUNCHLINE starter schema
-- Designed for v0 live engine, with fields that anticipate v1/v2 without forcing implementation.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE rooms (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    code TEXT NOT NULL UNIQUE,
    host_player_id UUID,
    status TEXT NOT NULL DEFAULT 'lobby',
    content_tier TEXT NOT NULL DEFAULT 'party',
    max_players INT NOT NULL DEFAULT 8,
    score_limit INT NOT NULL DEFAULT 5,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE players (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    room_id UUID REFERENCES rooms(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    guest_token_hash TEXT NOT NULL,
    is_host BOOLEAN NOT NULL DEFAULT false,
    connected BOOLEAN NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ
);

ALTER TABLE rooms
ADD CONSTRAINT fk_rooms_host_player
FOREIGN KEY (host_player_id) REFERENCES players(id);

CREATE TABLE packs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    description TEXT,
    source TEXT NOT NULL DEFAULT 'original',
    tier TEXT NOT NULL DEFAULT 'party',
    status TEXT NOT NULL DEFAULT 'published',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE cards (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    pack_id UUID REFERENCES packs(id) ON DELETE CASCADE,
    card_type TEXT NOT NULL CHECK (card_type IN ('prompt', 'answer')),
    text TEXT NOT NULL,
    blanks INT NOT NULL DEFAULT 1,
    source TEXT NOT NULL DEFAULT 'original',
    tier TEXT NOT NULL DEFAULT 'party',
    status TEXT NOT NULL DEFAULT 'approved',
    play_count INT NOT NULL DEFAULT 0,
    win_count INT NOT NULL DEFAULT 0,
    skip_count INT NOT NULL DEFAULT 0,
    report_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE room_packs (
    room_id UUID REFERENCES rooms(id) ON DELETE CASCADE,
    pack_id UUID REFERENCES packs(id) ON DELETE CASCADE,
    PRIMARY KEY (room_id, pack_id)
);

CREATE TABLE games (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    room_id UUID REFERENCES rooms(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'active',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at TIMESTAMPTZ
);

CREATE TABLE rounds (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    game_id UUID REFERENCES games(id) ON DELETE CASCADE,
    round_number INT NOT NULL,
    phase TEXT NOT NULL,
    prompt_card_id UUID REFERENCES cards(id),
    judge_player_id UUID REFERENCES players(id),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at TIMESTAMPTZ
);

CREATE TABLE player_hands (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    game_id UUID REFERENCES games(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    card_id UUID REFERENCES cards(id),
    position INT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    dealt_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE submissions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    round_id UUID REFERENCES rounds(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(round_id, player_id)
);

CREATE TABLE submission_cards (
    submission_id UUID REFERENCES submissions(id) ON DELETE CASCADE,
    card_id UUID REFERENCES cards(id),
    position INT NOT NULL,
    PRIMARY KEY (submission_id, card_id)
);

CREATE TABLE scores (
    game_id UUID REFERENCES games(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    points INT NOT NULL DEFAULT 0,
    PRIMARY KEY (game_id, player_id)
);

CREATE TABLE round_winners (
    round_id UUID PRIMARY KEY REFERENCES rounds(id) ON DELETE CASCADE,
    submission_id UUID REFERENCES submissions(id),
    player_id UUID REFERENCES players(id),
    picked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- v1 daily mode

CREATE TABLE daily_groups (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name TEXT NOT NULL,
    invite_code TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE daily_memberships (
    group_id UUID REFERENCES daily_groups(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, player_id)
);

CREATE TABLE daily_rounds (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    group_id UUID REFERENCES daily_groups(id) ON DELETE CASCADE,
    prompt_card_id UUID REFERENCES cards(id),
    round_date DATE NOT NULL,
    status TEXT NOT NULL DEFAULT 'open_for_submissions',
    reveal_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(group_id, round_date)
);

CREATE TABLE daily_submissions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    daily_round_id UUID REFERENCES daily_rounds(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    answer_text TEXT,
    answer_card_id UUID REFERENCES cards(id),
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(daily_round_id, player_id)
);

CREATE TABLE daily_votes (
    daily_round_id UUID REFERENCES daily_rounds(id) ON DELETE CASCADE,
    voter_player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    submission_id UUID REFERENCES daily_submissions(id) ON DELETE CASCADE,
    voted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (daily_round_id, voter_player_id)
);

-- v2 content platform

CREATE TABLE generation_jobs (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    theme TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    requested_by UUID,
    output_pack_id UUID REFERENCES packs(id),
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE card_reports (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    card_id UUID REFERENCES cards(id) ON DELETE CASCADE,
    reporter_player_id UUID REFERENCES players(id),
    reason TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ
);

CREATE TABLE card_ratings (
    card_id UUID REFERENCES cards(id) ON DELETE CASCADE,
    player_id UUID REFERENCES players(id) ON DELETE CASCADE,
    rating INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (card_id, player_id)
);

CREATE INDEX idx_rooms_code ON rooms(code);
CREATE INDEX idx_players_room_id ON players(room_id);
CREATE INDEX idx_cards_pack_id ON cards(pack_id);
CREATE INDEX idx_cards_type_status ON cards(card_type, status);
CREATE INDEX idx_rounds_game_id ON rounds(game_id);
CREATE INDEX idx_submissions_round_id ON submissions(round_id);
CREATE INDEX idx_daily_rounds_group_date ON daily_rounds(group_id, round_date);
