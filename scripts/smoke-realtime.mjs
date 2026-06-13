#!/usr/bin/env node

const target = (process.argv[2] || process.env.PUNCHLINE_BASE_URL || 'http://127.0.0.1:8080').replace(/\/$/, '');
const wsBase = target.replace(/^http/, 'ws');
const deadlineMs = Number(process.env.PUNCHLINE_SMOKE_TIMEOUT_MS || 8000);

function now() {
  return Date.now();
}

async function post(path, body) {
  const started = now();
  const res = await fetch(`${target}${path}`, {
    method: 'POST',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(`${path} returned ${res.status}: ${JSON.stringify(data)}`);
  }
  return { data, ms: now() - started };
}

function openClient(code, player, token) {
  const url = `${wsBase}/ws/rooms/${code}?player_id=${encodeURIComponent(player.id)}&token=${encodeURIComponent(token)}`;
  const ws = new WebSocket(url);
  const client = {
    name: player.name,
    player,
    room: null,
    messages: [],
    send(type, payload = {}) {
      ws.send(JSON.stringify({ type, ...payload }));
    },
    close() {
      ws.close();
    },
  };
  ws.addEventListener('message', async (ev) => {
    const msg = JSON.parse(await ev.data.text?.() || ev.data);
    client.messages.push(msg);
    if (msg.room) client.room = msg.room;
    if (msg.error) client.error = msg.error;
  });
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`websocket open timed out for ${player.name}`)), deadlineMs);
    ws.addEventListener('open', () => {
      clearTimeout(timeout);
      client.ws = ws;
      resolve(client);
    });
    ws.addEventListener('error', () => {
      clearTimeout(timeout);
      reject(new Error(`websocket failed for ${player.name}`));
    });
  });
}

async function waitFor(label, predicate) {
  const started = now();
  while (now() - started < deadlineMs) {
    const result = predicate();
    if (result) return result;
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`timed out waiting for ${label}`);
}

function ownHand(client) {
  return client.room?.players?.find((p) => p.id === client.player.id)?.hand || [];
}

const timings = {};
const clients = [];

try {
  let result = await post('/api/rooms');
  timings.createRoomMs = result.ms;
  const code = result.data.code;

  const joined = [];
  for (const name of ['Host', 'Bob', 'Carol']) {
    result = await post(`/api/rooms/${code}/join`, { name });
    timings[`join${name}Ms`] = result.ms;
    joined.push(result.data);
  }

  for (const entry of joined) {
    clients.push(await openClient(code, entry.player, entry.token));
  }
  await waitFor('all clients to receive lobby', () => clients.every((c) => c.room?.phase === 'lobby'));

  const host = clients[0];
  const startedAt = now();
  host.send('start_game');
  await waitFor('submitting phase', () => clients.every((c) => c.room?.phase === 'submitting'));
  timings.startToSubmitMs = now() - startedAt;

  const judgeID = host.room.judge_id;
  const answerers = clients.filter((c) => c.player.id !== judgeID);
  for (const client of answerers) {
    const card = ownHand(client)[0];
    if (!card) throw new Error(`${client.name} has no card to submit`);
    client.send('submit_answer', { answer_card_id: card.id });
  }
  const judgingAt = now();
  await waitFor('judging phase', () => clients.every((c) => c.room?.phase === 'judging'));
  timings.submitToJudgeMs = now() - judgingAt;

  const judge = clients.find((c) => c.player.id === judgeID);
  const firstSubmission = judge.room.submissions?.[0];
  if (!firstSubmission?.id) throw new Error('judge did not receive a submission');
  const pickAt = now();
  judge.send('pick_winner', { submission_id: firstSubmission.id });
  await waitFor('scoring phase', () => clients.every((c) => c.room?.phase === 'scoring'));
  timings.pickToScoringMs = now() - pickAt;

  const winner = clients[0].room.players.find((p) => p.score === 1);
  if (!winner) throw new Error('winner score was not awarded');

  const nextAt = now();
  host.send('next_round');
  await waitFor('next round', () => clients.every((c) => c.room?.phase === 'submitting' && c.room.round_number === 2));
  timings.nextRoundMs = now() - nextAt;

  const rotatedJudge = clients[0].room.judge_id;
  if (rotatedJudge === judgeID) throw new Error('judge did not rotate');

  console.log(JSON.stringify({
    ok: true,
    target,
    code,
    winner: winner.name,
    phase: clients[0].room.phase,
    round: clients[0].room.round_number,
    playerCount: clients[0].room.players.length,
    timings,
  }, null, 2));
} finally {
  for (const client of clients) client.close();
}
