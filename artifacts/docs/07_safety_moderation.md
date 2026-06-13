# Safety and Moderation

## Product stance

Safety is not a compliance checkbox. It expands the number of rooms where the game can be played.

The same engine should be usable by:

- close friends
- family
- coworkers
- streamers
- chaotic late-night groups

## Content tiers

```txt
FAMILY
PARTY
UNFILTERED
```

Room setting determines the maximum allowed tier.

## Room controls

Host controls:

- content tier
- allow community packs
- allow AI packs
- allow unreviewed/generated cards
- skip card
- ban card from room
- end round

## In-game controls

Players should have:

- pass/skip option
- report card
- vote-to-skip if enough players object

## Moderation statuses

```txt
PENDING
REVIEWED
ACTIONED
DISMISSED
APPEALED
```

## Moderation actions

```txt
NO_ACTION
RETIRE_CARD
DOWNRANK_CARD
BAN_PACK
WARN_AUTHOR
BAN_AUTHOR
```

## Provenance

Every card should expose its source internally:

```txt
official_original
ai_generated
community_authored
topical_generated
```

This allows filtering, trust, and debugging.
