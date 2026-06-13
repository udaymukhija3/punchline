import React, { useEffect, useRef, useState, useCallback } from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

// Same-origin everywhere: in dev Vite proxies /api and /ws to the backend; in
// prod the Go server serves this bundle and handles both. Optional override for
// split-origin setups.
const API = import.meta.env.VITE_API_URL || '';
function wsURL(code, playerID, token) {
  const params = new URLSearchParams({ player_id: playerID, token });
  if (API) {
    const base = API.replace(/^http/, 'ws');
    return `${base}/ws/rooms/${code}?${params.toString()}`;
  }
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${location.host}/ws/rooms/${code}?${params.toString()}`;
}

const SESSION_KEY = 'punchline.session';
const loadSession = () => { try { return JSON.parse(localStorage.getItem(SESSION_KEY)); } catch { return null; } };
const saveSession = (s) => localStorage.setItem(SESSION_KEY, JSON.stringify(s));
const clearSession = () => localStorage.removeItem(SESSION_KEY);
const normalizeRoomCode = (value) => value.replace(/[^a-z0-9]/gi, '').slice(0, 4).toUpperCase();
const MIN_PLAYERS = 3;

const demoBuilt = [
  'Live rooms with invite links and 4-character room codes.',
  'A full 3+ player loop: lobby, prompts, private hands, judging, scoring, and play again.',
  'Reconnectable guest sessions, host settings, content tiers, and a larger seed deck.',
];

const demoRunSteps = [
  ['Backend', ['cd backend', 'go run ./cmd/api']],
  ['Frontend', ['cd frontend', 'npm install', 'npm run dev']],
  ['Open', ['http://localhost:5173']],
];

const demoVerify = [
  'Open three browser tabs or phones.',
  'Create a room, join with two names, then start the game.',
  'Submit answers, pick a winner, and confirm the next round rotates the judge.',
];

async function readPayload(res) {
  const text = await res.text();
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return {};
  }
}

function errorMessage(err, fallback) {
  if (err instanceof TypeError) return 'Could not reach the server. Check your connection and try again.';
  return err?.message || fallback;
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

function App() {
  const [session, setSession] = useState(loadSession);
  const [room, setRoom] = useState(null);
  const [status, setStatus] = useState('idle'); // idle | connecting | connected | closed
  const [error, setError] = useState('');
  const [busyAction, setBusyAction] = useState('');
  const ws = useRef(null);
  const retries = useRef(0);
  const everOpened = useRef(false);
  const wantConnected = useRef(false);

  const disconnect = useCallback(() => {
    wantConnected.current = false;
    if (ws.current) { ws.current.onclose = null; ws.current.close(); ws.current = null; }
  }, []);

  const connect = useCallback((sess) => {
    if (!sess?.token) {
      clearSession();
      setSession(null);
      setRoom(null);
      setError('Session expired — join the room again.');
      return;
    }
    wantConnected.current = true;
    if (ws.current) { ws.current.onclose = null; ws.current.close(); }
    setStatus('connecting');
    const sock = new WebSocket(wsURL(sess.code, sess.playerId, sess.token));
    sock.onopen = () => { everOpened.current = true; retries.current = 0; setStatus('connected'); setError(''); };
    sock.onmessage = (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.error) setError(msg.error);
      if (msg.room) setRoom(msg.room);
    };
    sock.onclose = () => {
      if (!wantConnected.current) return;
      setStatus('closed');
      retries.current += 1;
      if (retries.current > 6) {
        // Room is gone (e.g. server restarted) or network is down for good.
        clearSession();
        setSession(null);
        setRoom(null);
        setError(everOpened.current ? 'Disconnected — that room has ended.' : 'Could not find that room.');
        wantConnected.current = false;
        return;
      }
      setTimeout(() => { if (wantConnected.current) connect(sess); }, Math.min(1500 * retries.current, 5000));
    };
    ws.current = sock;
  }, []);

  // Reconnect automatically on load / refresh using the stored session.
  useEffect(() => {
    if (session) connect(session);
    return () => disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const send = (type, payload = {}) => ws.current?.send(JSON.stringify({ type, ...payload }));

  async function joinCreatedRoom(code, name) {
    const c = normalizeRoomCode(code);
    const res = await fetch(`${API}/api/rooms/${c}/join`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }),
    });
    const data = await readPayload(res);
    if (!res.ok) throw new Error(data.error || 'Could not join that room.');
    if (!data?.player?.id || !data?.token || !data?.room) throw new Error('The server returned an incomplete room session.');
    const sess = { code: c, playerId: data.player.id, token: data.token, name };
    everOpened.current = false; retries.current = 0;
    saveSession(sess); setSession(sess); setRoom(data.room); connect(sess);
  }

  async function createAndJoin(name) {
    setError('');
    setBusyAction('create');
    try {
      const res = await fetch(`${API}/api/rooms`, { method: 'POST' });
      const data = await readPayload(res);
      if (!res.ok) throw new Error(data.error || 'Could not create a room.');
      if (!data?.code) throw new Error('The server did not return a room code.');
      await joinCreatedRoom(data.code, name);
    } catch (err) {
      setError(errorMessage(err, 'Could not create a room.'));
    } finally {
      setBusyAction('');
    }
  }

  async function join(code, name) {
    setError('');
    const c = normalizeRoomCode(code);
    if (c.length !== 4) {
      setError('Enter the 4-character room code.');
      return;
    }
    setBusyAction('join');
    try {
      await joinCreatedRoom(c, name);
    } catch (err) {
      setError(errorMessage(err, 'Could not join that room.'));
    } finally {
      setBusyAction('');
    }
  }

  function leave() {
    disconnect(); clearSession(); setSession(null); setRoom(null); setError(''); setStatus('idle');
  }

  if (!session || !room) {
    return <Landing onCreate={createAndJoin} onJoin={join} error={error} reconnecting={!!session} busyAction={busyAction} />;
  }
  return <Game room={room} me={session.playerId} status={status} error={error} send={send} onLeave={leave} />;
}

function Landing({ onCreate, onJoin, error, reconnecting, busyAction }) {
  const [name, setName] = useState(localStorage.getItem('punchline.name') || '');
  const [code, setCode] = useState(normalizeRoomCode(new URLSearchParams(location.search).get('join') || ''));
  const remember = (n) => { setName(n); localStorage.setItem('punchline.name', n); };
  const cleanName = name.trim();
  const canCreate = !!cleanName && !busyAction;
  const canJoin = !!cleanName && code.length === 4 && !busyAction;

  if (reconnecting) {
    return <main className="shell center"><div className="logo">Punchline</div><p className="muted">Reconnecting to your room...</p></main>;
  }
  return (
    <main className="shell landing">
      <section className="landing-hero">
        <div className="brand-block">
          <div className="logo">Punchline</div>
          <p className="tagline">A demo build of a live, browser-based party game.</p>
          <p className="demo-note">Use this page to start a room, then use the checklist below to verify the product flow.</p>
        </div>
        <div className="panel" aria-busy={!!busyAction}>
          <label>Your name</label>
          <input className="field" placeholder="e.g. Sam" value={name} maxLength={20} autoComplete="nickname" onChange={(e) => remember(e.target.value)} />
          <button className="btn primary block" disabled={!canCreate} onClick={() => onCreate(cleanName)}>
            {busyAction === 'create' ? 'Creating room...' : 'Create a room'}
          </button>
          <div className="divider"><span>or join one</span></div>
          <div className="row">
            <input className="field code-input" placeholder="Code" value={code} maxLength={4} autoCapitalize="characters" autoComplete="off"
                   onChange={(e) => setCode(normalizeRoomCode(e.target.value))} />
            <button className="btn block" disabled={!canJoin} onClick={() => onJoin(code, cleanName)}>
              {busyAction === 'join' ? 'Joining...' : 'Join'}
            </button>
          </div>
          {error && <p className="err" role="alert">{error}</p>}
        </div>
      </section>

      <section className="demo-guide" aria-label="Demo guide">
        <div className="demo-section">
          <h2>Built</h2>
          <ul className="plain-list">
            {demoBuilt.map((item) => <li key={item}>{item}</li>)}
          </ul>
        </div>
        <div className="demo-section">
          <h2>Run locally</h2>
          <ol className="run-steps">
            {demoRunSteps.map(([label, commands]) => (
              <li key={label}>
                <span>{label}</span>
                <div className="command-stack">
                  {commands.map((command) => <code key={command}>{command}</code>)}
                </div>
              </li>
            ))}
          </ol>
        </div>
        <div className="demo-section">
          <h2>Verify</h2>
          <ol className="plain-list numbered">
            {demoVerify.map((item) => <li key={item}>{item}</li>)}
          </ol>
          <p className="muted small">Best with 3+ players. Works on phones. No install.</p>
        </div>
      </section>
    </main>
  );
}

function useCountdown(deadline) {
  const [, tick] = useState(0);
  useEffect(() => {
    if (!deadline) return;
    const id = setInterval(() => tick((n) => n + 1), 500);
    return () => clearInterval(id);
  }, [deadline]);
  if (!deadline) return null;
  return Math.max(0, Math.round((new Date(deadline).getTime() - Date.now()) / 1000));
}

function Game({ room, me, status, error, send, onLeave }) {
  const seconds = useCountdown(room.phase_deadline);
  const meP = room.players.find((p) => p.id === me) || {};
  const isHost = room.host_id === me;
  const isJudge = !!meP.is_judge;
  const judge = room.players.find((p) => p.is_judge);
  const answerers = room.players.filter((p) => !p.is_judge);
  const submittedCount = answerers.filter((p) => p.submitted).length;
  const neededPlayers = Math.max(0, MIN_PLAYERS - room.players.length);
  const shareLink = `${location.origin}/?join=${room.code}`;
  const [inviteNotice, setInviteNotice] = useState('');
  const inviteTimer = useRef(null);

  useEffect(() => () => clearTimeout(inviteTimer.current), []);

  const showInviteNotice = useCallback((message) => {
    clearTimeout(inviteTimer.current);
    setInviteNotice(message);
    inviteTimer.current = setTimeout(() => setInviteNotice(''), 3500);
  }, []);

  const copyLink = useCallback(async () => {
    try {
      if (!navigator.clipboard) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(shareLink);
      showInviteNotice('Invite link copied.');
    } catch {
      showInviteNotice(`Copy failed. Use this link: ${shareLink}`);
    }
  }, [shareLink, showInviteNotice]);

  const shareRoom = useCallback(async () => {
    try {
      if (navigator.share) {
        await navigator.share({ title: 'Join my Punchline room', text: `Room ${room.code}`, url: shareLink });
        showInviteNotice('Invite ready to send.');
        return;
      }
      await copyLink();
    } catch (err) {
      if (err?.name !== 'AbortError') showInviteNotice(`Copy failed. Use this link: ${shareLink}`);
    }
  }, [copyLink, room.code, shareLink, showInviteNotice]);

  return (
    <main className="shell game">
      <header className="bar">
        <div className="room-id">
          <span className="muted small">Room</span>
          <button className="code-pill" title="Copy invite link" aria-label={`Copy invite link for room ${room.code}`} onClick={copyLink}>{room.code}</button>
        </div>
        <div className="bar-mid">
          <span className={`connection-state ${status}`}>
            <span className={`status-dot ${status}`} />
            {connectionLabel(status)}
          </span>
          <span className="phase-chip">{phaseLabel(room.phase)}</span>
          {room.round_number > 0 && <span className="muted small">Round {room.round_number}</span>}
          {seconds != null && (room.phase === 'submitting' || room.phase === 'judging') && (
            <span className={`timer ${seconds <= 10 ? 'low' : ''}`}>{seconds}s</span>
          )}
        </div>
        <div className="bar-actions">
          <button className="btn small" onClick={shareRoom}>Invite</button>
          <button className="btn ghost small" onClick={onLeave}>Leave</button>
        </div>
      </header>

      {error && <p className="err banner">{error}</p>}
      {inviteNotice && <p className="share-notice" aria-live="polite">{inviteNotice}</p>}

      <section className="layout">
        <aside className="scoreboard card">
          <div className="panel-head">
            <h3>Players</h3>
            <span className="muted small">{room.players.length}/{room.max_players}</span>
          </div>
          <p className="panel-note">First to {room.score_limit}</p>
          {room.players.map((p) => (
            <div className={`player-row ${p.id === me ? 'you' : ''}`} key={p.id}>
              <div className="player-main">
                <span className="player-name">{p.name}</span>
                <RoleBadges player={p} room={room} me={me} />
              </div>
              <div className="player-meta">
                {room.phase === 'submitting'
                  ? <span className={`sub-status ${playerRoundStatus(p, room.phase)}`}>{playerRoundLabel(p)}</span>
                  : <span className={`conn-label ${p.connected ? 'on' : 'off'}`}>{p.connected ? 'Online' : 'Away'}</span>}
                <span className="score">{p.score}</span>
              </div>
            </div>
          ))}
          {room.phase === 'lobby' && (
            isHost
              ? <button className="btn primary block" disabled={neededPlayers > 0} onClick={() => send('start_game')}>
                  {neededPlayers > 0 ? `Need ${plural(neededPlayers, 'more player')}` : 'Start game'}
                </button>
              : <p className="muted small">Waiting for the host to start...</p>
          )}
          {isHost && room.phase !== 'lobby' && room.phase !== 'finished' && (
            <button className="btn ghost block small" onClick={() => send('end_game')}>End game</button>
          )}
        </aside>

        <div className="stage card">
          {room.phase === 'lobby' && (
            <div className="center-stage">
              <h2>{isHost ? 'Build the room' : 'You are in'}</h2>
              <p className="stage-copy">
                {isHost
                  ? neededPlayers > 0
                    ? `Invite ${plural(neededPlayers, 'more player')} to start.`
                    : 'The room is ready. Start when everyone has settled.'
                  : 'Keep this tab open. The host starts the first round.'}
              </p>
              <button className="big-code" onClick={copyLink}>{room.code}</button>
              <div className="invite-actions">
                <button className="btn primary" onClick={shareRoom}>Invite players</button>
                <button className="btn" onClick={copyLink}>Copy link</button>
              </div>
              <RoomSettings room={room} isHost={isHost} send={send} />
            </div>
          )}

          {room.phase === 'finished' && <Finished room={room} isHost={isHost} send={send} />}

          {(room.phase === 'submitting' || room.phase === 'judging' || room.phase === 'scoring') && (
            <>
              <PhasePanel
                room={room}
                isHost={isHost}
                isJudge={isJudge}
                meP={meP}
                judge={judge}
                submittedCount={submittedCount}
                answererCount={answerers.length}
                send={send}
              />
              <div className="prompt-card">
                <span className="prompt-label">Prompt</span>
                <p>{room.prompt?.text}</p>
              </div>
              <div className="submissions">
                {room.submissions?.map((s) => (
                  <div className={`submission ${s.is_winner ? 'winner' : ''}`} key={s.id}>
                    <span>{room.phase === 'submitting' ? 'Card submitted' : s.answer?.text}</span>
                    {isJudge && room.phase === 'judging' && (
                      <button className="btn small" onClick={() => send('pick_winner', { submission_id: s.id })}>Pick</button>
                    )}
                    {s.is_winner && s.player_name && <span className="by">{s.player_name} +1</span>}
                  </div>
                ))}
                {room.phase === 'submitting' && room.submissions?.length === 0 && <p className="muted">No answers in yet...</p>}
              </div>
              {room.phase === 'scoring' && isHost && (
                <button className="btn primary block" onClick={() => send('next_round')}>Next round</button>
              )}
              {room.phase === 'scoring' && !isHost && <p className="muted">Waiting for the host to start the next round...</p>}
            </>
          )}
        </div>

        <aside className="hand card">
          <div className="panel-head">
            <h3>{room.phase === 'lobby' ? 'Your cards' : 'Your hand'}</h3>
            {!isJudge && room.phase === 'submitting' && meP.submitted && <span className="locked-pill">Locked</span>}
          </div>
          {isJudge && room.phase !== 'lobby'
            ? <p className="muted">You are judging this round.</p>
            : (meP.hand || []).map((c) => (
                <button className={`answer-card ${meP.submitted ? 'muted-card' : ''}`} key={c.id}
                        disabled={room.phase !== 'submitting' || isJudge || meP.submitted}
                        onClick={() => send('submit_answer', { answer_card_id: c.id })}>
                  {c.text}
                </button>
              ))}
          {room.phase === 'lobby' && <p className="muted small">These stay private. Pick one when the first prompt lands.</p>}
          {!isJudge && room.phase === 'submitting' && meP.submitted && <p className="muted small">Your answer is in.</p>}
          {!isJudge && room.phase === 'submitting' && !meP.submitted && (meP.hand || []).length > 0 && <p className="muted small">Choose one answer card.</p>}
        </aside>
      </section>
    </main>
  );
}

function PhasePanel({ room, isHost, isJudge, meP, judge, submittedCount, answererCount, send }) {
  const judgeName = judge?.name || 'The judge';
  let title = '';
  let body = '';

  if (room.phase === 'submitting') {
    if (isJudge) {
      title = 'You judge this round';
      body = `${submittedCount}/${answererCount} answers are in. Cards reveal when everyone answers or the timer ends.`;
    } else if (meP.submitted) {
      title = 'Answer locked in';
      body = `${submittedCount}/${answererCount} answers are in. The judge will see cards without names.`;
    } else {
      title = 'Choose your answer';
      body = `${judgeName} is judging. Pick one card before the timer runs out.`;
    }
  }

  if (room.phase === 'judging') {
    title = isJudge ? 'Pick the winner' : `${judgeName} is choosing`;
    body = isJudge ? 'Cards are anonymous here. Tap the answer that lands hardest.' : 'Answers are revealed, but authors stay hidden until the pick.';
  }

  if (room.phase === 'scoring') {
    title = 'Round reveal';
    body = isHost ? 'Start the next round when the room has seen the winner.' : 'The host starts the next round.';
  }

  return (
    <div className="phase-panel">
      <div>
        <h2>{title}</h2>
        <p>{body}</p>
      </div>
      {room.phase === 'submitting' && (
        <div className="phase-actions">
          <span className="answered-count">{submittedCount}/{answererCount} in</span>
          {isHost && <button className="btn ghost small" onClick={() => send('skip_prompt')}>Skip prompt</button>}
        </div>
      )}
    </div>
  );
}

function RoleBadges({ player, room, me }) {
  return (
    <span className="role-stack">
      {player.id === me && <span className="role-badge you-badge">You</span>}
      {player.id === room.host_id && <span className="role-badge">Host</span>}
      {player.is_judge && <span className="role-badge judge-badge">Judge</span>}
    </span>
  );
}

function playerRoundStatus(player, phase) {
  if (phase !== 'submitting') return '';
  if (player.is_judge) return 'judge';
  return player.submitted ? 'in' : 'waiting';
}

function playerRoundLabel(player) {
  if (player.is_judge) return 'Judge';
  return player.submitted ? 'In' : 'Choosing';
}

function connectionLabel(status) {
  return { idle: 'Idle', connecting: 'Connecting', connected: 'Live', closed: 'Reconnecting' }[status] || status;
}

function RoomSettings({ room, isHost, send }) {
  const update = (patch) => send('update_settings', { settings: patch });
  const playerCount = room.players.length;

  if (!isHost) {
    return (
      <div className="settings readonly">
        <h3>Game settings</h3>
        <div className="settings-summary">
          <span>First to <b>{room.score_limit}</b></span>
          <span>Timer <b>{room.round_seconds}s</b></span>
          <span>Max <b>{room.max_players}</b></span>
          <span>Content <b>{room.content_tier}</b></span>
        </div>
      </div>
    );
  }
  return (
    <div className="settings">
      <h3>Game settings</h3>
      <div className="settings-grid">
        <label>Win at
          <select value={room.score_limit} onChange={(e) => update({ score_limit: +e.target.value })}>
            {[3, 5, 7, 10].map((n) => <option key={n} value={n}>{n} points</option>)}
          </select>
        </label>
        <label>Answer timer
          <select value={room.round_seconds} onChange={(e) => update({ round_seconds: +e.target.value })}>
            {[30, 60, 90, 120].map((n) => <option key={n} value={n}>{n}s</option>)}
          </select>
        </label>
        <label>Max players
          <select value={room.max_players} onChange={(e) => update({ max_players: +e.target.value })}>
            {[4, 6, 8, 12].map((n) => <option key={n} value={n} disabled={n < playerCount}>{n}</option>)}
          </select>
        </label>
        <label>Content
          <div className="tier-toggle">
            {['family', 'party'].map((t) => (
              <button key={t} type="button"
                      className={`btn small ${room.content_tier === t ? 'primary' : 'ghost'}`}
                      onClick={() => update({ content_tier: t })}>{t}</button>
            ))}
          </div>
        </label>
      </div>
    </div>
  );
}

function Finished({ room, isHost, send }) {
  const sorted = [...room.players].sort((a, b) => b.score - a.score);
  const winner = sorted[0];
  return (
    <div className="center-stage">
      <h2>{winner?.name} wins</h2>
      <p className="stage-copy">Final score</p>
      <div className="final-scores">
        {sorted.map((p, i) => (
          <div className="player-row" key={p.id}><span className="who">{i + 1}. {p.name}</span><span className="score">{p.score}</span></div>
        ))}
      </div>
      {isHost
        ? <button className="btn primary block" onClick={() => send('play_again')}>Play again</button>
        : <p className="muted">Waiting for the host to start a new game...</p>}
    </div>
  );
}

function phaseLabel(phase) {
  return { lobby: 'Lobby', submitting: 'Answering', judging: 'Judging', scoring: 'Reveal', finished: 'Game over' }[phase] || phase;
}

createRoot(document.getElementById('root')).render(<App />);
