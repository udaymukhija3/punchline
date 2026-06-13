# Claude Review Prompt

Review this PUNCHLINE implementation as if you are a senior backend engineer evaluating it for portfolio quality.

Focus on:

1. Whether the room actor/state-machine design is clean and maintainable.
2. Whether WebSocket commands/events are well modeled.
3. Whether reconnection is robust.
4. Whether the PostgreSQL schema supports v0 without over-engineering.
5. Whether client/server responsibilities are properly separated.
6. Whether the game loop can survive bad client behavior.
7. Whether there are obvious race conditions.
8. Whether this project would read as a serious backend/systems project on a resume.

Be blunt. Identify what to cut, what to simplify, what to harden, and what to test.

Do not suggest AI features, marketplace, stream mode, or daily mode until v0 is solid.
