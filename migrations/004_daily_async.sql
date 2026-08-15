CREATE TYPE daily_round_state AS ENUM (
    'OPEN_FOR_SUBMISSIONS',
    'REVEALED_FOR_VOTING',
    'FINALIZED'
);

CREATE TABLE daily_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL UNIQUE CHECK (code ~ '^[A-Z0-9]{6}$'),
    name TEXT NOT NULL CHECK (char_length(name) BETWEEN 2 AND 40),
    timezone TEXT NOT NULL,
    reveal_hour SMALLINT NOT NULL DEFAULT 18 CHECK (reveal_hour BETWEEN 0 AND 23),
    max_members SMALLINT NOT NULL DEFAULT 30 CHECK (max_members BETWEEN 2 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE daily_memberships (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id UUID NOT NULL REFERENCES daily_groups(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 20),
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(id, group_id)
);

CREATE UNIQUE INDEX idx_daily_memberships_group_name
    ON daily_memberships(group_id, lower(display_name));
CREATE INDEX idx_daily_memberships_group ON daily_memberships(group_id);

CREATE TABLE daily_rounds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id UUID NOT NULL REFERENCES daily_groups(id) ON DELETE CASCADE,
    round_number INT NOT NULL CHECK (round_number > 0),
    round_date DATE NOT NULL,
    prompt_key TEXT NOT NULL,
    prompt_text TEXT NOT NULL CHECK (char_length(prompt_text) BETWEEN 1 AND 500),
    state daily_round_state NOT NULL DEFAULT 'OPEN_FOR_SUBMISSIONS',
    opens_at TIMESTAMPTZ NOT NULL,
    reveal_at TIMESTAMPTZ NOT NULL,
    voting_closes_at TIMESTAMPTZ NOT NULL,
    finalized_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(group_id, round_date),
    UNIQUE(group_id, round_number),
    UNIQUE(id, group_id),
    CHECK (opens_at < reveal_at),
    CHECK (reveal_at < voting_closes_at),
    CHECK ((state = 'FINALIZED' AND finalized_at IS NOT NULL) OR state <> 'FINALIZED')
);

CREATE INDEX idx_daily_rounds_due_reveal
    ON daily_rounds(reveal_at) WHERE state = 'OPEN_FOR_SUBMISSIONS';
CREATE INDEX idx_daily_rounds_due_finalize
    ON daily_rounds(voting_closes_at) WHERE state = 'REVEALED_FOR_VOTING';
CREATE INDEX idx_daily_rounds_group_recent ON daily_rounds(group_id, round_date DESC);

CREATE TABLE daily_submissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id UUID NOT NULL REFERENCES daily_groups(id) ON DELETE CASCADE,
    daily_round_id UUID NOT NULL,
    membership_id UUID NOT NULL,
    answer_text TEXT NOT NULL CHECK (char_length(answer_text) BETWEEN 1 AND 160),
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(daily_round_id, membership_id),
    UNIQUE(id, daily_round_id),
    UNIQUE(id, group_id),
    FOREIGN KEY (daily_round_id, group_id)
        REFERENCES daily_rounds(id, group_id) ON DELETE CASCADE,
    FOREIGN KEY (membership_id, group_id)
        REFERENCES daily_memberships(id, group_id) ON DELETE CASCADE
);

CREATE INDEX idx_daily_submissions_round ON daily_submissions(daily_round_id);

CREATE TABLE daily_votes (
    group_id UUID NOT NULL REFERENCES daily_groups(id) ON DELETE CASCADE,
    daily_round_id UUID NOT NULL,
    voter_membership_id UUID NOT NULL,
    submission_id UUID NOT NULL,
    voted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (daily_round_id, voter_membership_id),
    FOREIGN KEY (daily_round_id, group_id)
        REFERENCES daily_rounds(id, group_id) ON DELETE CASCADE,
    FOREIGN KEY (voter_membership_id, group_id)
        REFERENCES daily_memberships(id, group_id) ON DELETE CASCADE,
    FOREIGN KEY (submission_id, daily_round_id)
        REFERENCES daily_submissions(id, daily_round_id) ON DELETE CASCADE
);

CREATE INDEX idx_daily_votes_submission ON daily_votes(submission_id);
