# Async Daily Engine

## Product job

Daily mode should make the group chat feel alive even when nobody schedules a game night.

## Core loop

```txt
Daily prompt appears
→ group members submit anytime
→ reveal happens when all submit or timer expires
→ members vote
→ winner announced
→ streaks update
→ share card generated
```

## Data model concepts

```txt
DailyGroup
DailyMembership
DailyPrompt
DailyRound
DailySubmission
DailyVote
DailyResult
DailyStreak
```

## Daily round states

```txt
OPEN_FOR_SUBMISSIONS
REVEALED_FOR_VOTING
COMPLETED
EXPIRED
```

## Key decisions

### Reveal policy

Recommended:

- reveal when everyone submits, or
- reveal at a fixed time, such as 9 PM local group time

This keeps daily mode from stalling.

### Voting policy

Recommended:

- democracy mode by default
- no single judge
- players cannot vote for themselves

### Share output

Spoiler-safe share card:

```txt
PUNCHLINE Daily #42
Winner: Uday
Prompt: blurred / hidden until opened
Group streak: 7 days
[Play today's prompt]
```

## Why daily matters

The live room is the spike of fun. Daily mode is the habit loop.

Without daily, the product only exists when someone organizes a game night.
