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

const tutorialSteps = [
  ['1', 'Read the prompt', 'One player judges while the room gets a ridiculous blank to fill.'],
  ['2', 'Play a card', 'Everyone else picks one answer from a private hand.'],
  ['3', 'Win the laugh', 'The judge chooses blind, scores update, and the judge rotates.'],
];

const roomBeats = [
  'Computer seats can teach the first round.',
  'Invite links still work when the room is ready.',
  'First to the score limit wins.',
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

// The server's strings are written for logs, not for someone standing in a
// kitchen holding a phone. Anything not listed here is already player-facing.
const PLAYER_COPY = {
  'room not found': 'No room with that code. Check the four characters and try again.',
  'room is full': 'That room is full.',
  'could not join room': 'Could not join that room. Try again in a moment.',
  'could not save room': 'Could not save the room. Try again in a moment.',
  'could not create room': 'Could not create a room. Try again in a moment.',
  'too many requests': 'That was a lot of taps. Give it a minute.',
  'too many messages': 'That was a lot of taps. Give it a minute.',
};

function errorMessage(err, fallback) {
  if (err instanceof TypeError) return 'Could not reach the server. Check your connection and try again.';
  const raw = err?.message || '';
  return PLAYER_COPY[raw.toLowerCase()] || raw || fallback;
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? '' : 's'}`;
}

function App() {
  if (location.pathname.startsWith('/admin')) return <AdminApp />;
  return location.pathname.startsWith('/daily') ? <DailyApp /> : <LiveApp />;
}

// A link to a room you are not in beats the room you are in: clicking a
// friend's invite used to drop you back into your old room with no sign the
// link had been read at all.
function invitedRoomCode() {
  return normalizeRoomCode(new URLSearchParams(location.search).get('join') || '');
}

function LiveApp() {
  const [session, setSession] = useState(() => {
    const stored = loadSession();
    const invited = invitedRoomCode();
    return invited && stored?.code !== invited ? null : stored;
  });
  const [room, setRoom] = useState(null);
  const [status, setStatus] = useState('idle'); // idle | connecting | connected | closed
  const [error, setError] = useState('');
  const [busyAction, setBusyAction] = useState('');
  const ws = useRef(null);
  const retries = useRef(0);
  const everOpened = useRef(false);
  const wantConnected = useRef(false);
  const errorTimer = useRef(null);
  // setBusyAction only lands on the next render, so two taps in the same frame
  // both pass the disabled check and create two rooms. This ref is the guard;
  // busyAction stays as the affordance.
  const inFlight = useRef(false);

  // A command error belongs to the tap that caused it and must not outlive it.
  // Terminal errors (session gone, room ended) are set without `transient` and
  // stay until the player acts.
  const showError = useCallback((message, transient = false) => {
    clearTimeout(errorTimer.current);
    setError(message);
    if (transient && message) errorTimer.current = setTimeout(() => setError(''), 6000);
  }, []);

  useEffect(() => () => clearTimeout(errorTimer.current), []);

  const disconnect = useCallback(() => {
    wantConnected.current = false;
    if (ws.current) { ws.current.onclose = null; ws.current.close(); ws.current = null; }
  }, []);

  const connect = useCallback((sess) => {
    if (!sess?.token) {
      clearSession();
      setSession(null);
      setRoom(null);
      showError('Session expired — join the room again.');
      return;
    }
    wantConnected.current = true;
    if (ws.current) { ws.current.onclose = null; ws.current.close(); }
    setStatus('connecting');
    const sock = new WebSocket(wsURL(sess.code, sess.playerId, sess.token));
    sock.onopen = () => { everOpened.current = true; retries.current = 0; setStatus('connected'); showError(''); };
    sock.onmessage = (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.error) showError(PLAYER_COPY[msg.error.toLowerCase()] || msg.error, true);
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
        showError(everOpened.current ? 'Disconnected — that room has ended.' : 'Could not find that room.');
        wantConnected.current = false;
        return;
      }
      setTimeout(() => { if (wantConnected.current) connect(sess); }, Math.min(1500 * retries.current, 5000));
    };
    ws.current = sock;
  }, [showError]);

  // Reconnect automatically on load / refresh using the stored session.
  useEffect(() => {
    if (session) connect(session);
    return () => disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const send = (type, payload = {}) => ws.current?.send(JSON.stringify({ type, ...payload }));

  function activateRoomSession(code, name, data, mode = 'friends') {
    if (!data?.player?.id || !data?.token || !data?.room) throw new Error('The server returned an incomplete room session.');
    const c = normalizeRoomCode(code || data.room?.code || '');
    if (c.length !== 4) throw new Error('The server did not return a room code.');
    const sess = { code: c, playerId: data.player.id, token: data.token, name, mode };
    everOpened.current = false; retries.current = 0;
    saveSession(sess); setSession(sess); setRoom(data.room); connect(sess);
  }

  async function joinCreatedRoom(code, name, mode = 'friends') {
    const c = normalizeRoomCode(code);
    const res = await fetch(`${API}/api/rooms/${c}/join`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }),
    });
    const data = await readPayload(res);
    if (!res.ok) throw new Error(data.error || 'Could not join that room.');
    activateRoomSession(c, name, data, mode);
  }

  async function createAndJoin(name) {
    if (inFlight.current) return;
    inFlight.current = true;
    showError('');
    setBusyAction('create');
    try {
      const res = await fetch(`${API}/api/rooms`, { method: 'POST' });
      const data = await readPayload(res);
      if (!res.ok) throw new Error(data.error || 'Could not create a room.');
      if (!data?.code) throw new Error('The server did not return a room code.');
      await joinCreatedRoom(data.code, name);
    } catch (err) {
      showError(errorMessage(err, 'Could not create a room.'));
    } finally {
      inFlight.current = false;
      setBusyAction('');
    }
  }

  async function playComputer(name) {
    if (inFlight.current) return;
    inFlight.current = true;
    showError('');
    setBusyAction('computer');
    try {
      const res = await fetch(`${API}/api/computer-room`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }),
      });
      const data = await readPayload(res);
      if (!res.ok) throw new Error(data.error || 'Could not start a computer room.');
      activateRoomSession(data.room?.code, name, data, 'computer');
    } catch (err) {
      showError(errorMessage(err, 'Could not start a computer room.'));
    } finally {
      inFlight.current = false;
      setBusyAction('');
    }
  }

  async function join(code, name) {
    if (inFlight.current) return;
    showError('');
    const c = normalizeRoomCode(code);
    if (c.length !== 4) {
      showError('Enter the 4-character room code.');
      return;
    }
    inFlight.current = true;
    setBusyAction('join');
    try {
      await joinCreatedRoom(c, name);
    } catch (err) {
      showError(errorMessage(err, 'Could not join that room.'));
    } finally {
      inFlight.current = false;
      setBusyAction('');
    }
  }

  function leave() {
    // Tell the room first: without this the seat stays occupied for the life of
    // the room and nobody can free it.
    send('leave');
    disconnect(); clearSession(); setSession(null); setRoom(null); showError(''); setStatus('idle');
  }

  if (!session || !room) {
    return <Landing onCreate={createAndJoin} onJoin={join} onComputer={playComputer} error={error} reconnecting={!!session} busyAction={busyAction} />;
  }
  return <Game room={room} me={session.playerId} session={session} status={status} error={error} send={send} onLeave={leave} />;
}

function Landing({ onCreate, onJoin, onComputer, error, reconnecting, busyAction }) {
  const [name, setName] = useState(localStorage.getItem('punchline.name') || '');
  const [code, setCode] = useState(normalizeRoomCode(new URLSearchParams(location.search).get('join') || ''));
  const remember = (n) => { setName(n); localStorage.setItem('punchline.name', n); };
  const cleanName = name.trim();
  const canPlayComputer = !!cleanName && !busyAction;
  const canCreate = !!cleanName && !busyAction;
  const canJoin = !!cleanName && code.length === 4 && !busyAction;

  if (reconnecting) {
    return <main className="shell center"><div className="logo">Punchline</div><p className="muted">Reconnecting to your room...</p></main>;
  }
  return (
    <main className="shell landing">
      <section className="landing-hero" aria-label="Punchline start">
        <div className="brand-block">
          <span className="eyebrow">Prompt. Answer. Judge. Repeat.</span>
          <div className="logo">Punchline</div>
          <p className="tagline">A live party game where the funniest answer wins the room.</p>
          <div className="tutorial-steps" aria-label="How a round works">
            {tutorialSteps.map(([number, title, body]) => (
              <div className="tutorial-step" key={title}>
                <span>{number}</span>
                <div>
                  <b>{title}</b>
                  <p>{body}</p>
                </div>
              </div>
            ))}
          </div>
        </div>
        <div className="panel start-panel" aria-busy={!!busyAction}>
          <div className="panel-title">
            <span className="eyebrow">Start here</span>
            <h1>Try a round first</h1>
          </div>
          <label htmlFor="player-name">Your name</label>
          <input id="player-name" className="field" placeholder="e.g. Sam" value={name} maxLength={20} autoComplete="nickname" onChange={(e) => remember(e.target.value)} />
          <button className="btn primary block cta" disabled={!canPlayComputer} onClick={() => onComputer(cleanName)}>
            {busyAction === 'computer' ? 'Starting computer room...' : 'Play with computer'}
          </button>
          <ul className="beat-list">
            {roomBeats.map((item) => <li key={item}>{item}</li>)}
          </ul>
          <div className="divider"><span>or bring people in</span></div>
          <button className="btn primary block" disabled={!canCreate} onClick={() => onCreate(cleanName)}>
            {busyAction === 'create' ? 'Creating room...' : 'Host friends'}
          </button>
          <div className="join-box">
            <input className="field code-input" aria-label="Room code" placeholder="Code" value={code} maxLength={4} autoCapitalize="characters" autoComplete="off"
                   onChange={(e) => setCode(normalizeRoomCode(e.target.value))} />
            <button className="btn block" disabled={!canJoin} onClick={() => onJoin(code, cleanName)}>
              {busyAction === 'join' ? 'Joining...' : 'Join'}
            </button>
          </div>
          <a className="btn ghost block daily-link" href="/daily">Play the daily async mode</a>
          {error && <p className="err" role="alert">{error}</p>}
        </div>
      </section>
    </main>
  );
}

// A card stays face down until the reveal reaches it. The flip animation hangs
// off the `revealed` class, so it fires exactly once — when the server turns
// that card over — and not on unrelated re-renders.
function submissionClass(submission, phase) {
  const classes = ['submission'];
  classes.push(phase !== 'submitting' && submission.revealed ? 'revealed' : 'face-down');
  if (submission.is_winner && phase === 'scoring') classes.push('winner');
  return classes.join(' ');
}

function cardFaceText(submission, phase) {
  if (phase === 'submitting') return 'Card submitted';
  return submission.revealed ? submission.answer?.text : '';
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

function Game({ room, me, session, status, error, send, onLeave }) {
  const seconds = useCountdown(room.phase_deadline);
  // A double tap is the default gesture on a phone. The disabled flags below
  // only flip once the server's snapshot lands, so without this the second tap
  // sends a duplicate command and the player gets a red error for tapping the
  // way phones expect. One command per player per round phase is enough.
  const sentFor = useRef('');
  const sendOnce = (type, payload = {}) => {
    const key = `${type}:${room.round_number}:${room.phase}`;
    if (sentFor.current === key) return;
    sentFor.current = key;
    send(type, payload);
  };
  const meP = room.players.find((p) => p.id === me) || {};
  const isHost = room.host_id === me;
  const isJudge = !!meP.is_judge;
  const judge = room.players.find((p) => p.is_judge);
  const answerers = room.players.filter((p) => !p.is_judge);
  const submittedCount = answerers.filter((p) => p.submitted).length;
  const neededPlayers = Math.max(0, MIN_PLAYERS - room.players.length);
  // The winning card gets its own spotlight at scoring, so the table below it
  // lists everything else rather than repeating the winner.
  const submissions = room.submissions || [];
  const winner = room.phase === 'scoring' ? submissions.find((s) => s.is_winner) : null;
  const others = winner ? submissions.filter((s) => s.id !== winner.id) : submissions;
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
                  : p.is_computer
                    ? <span className="conn-label bot">Computer</span>
                    : <span className={`conn-label ${p.connected ? 'on' : 'off'}`}>{p.connected ? 'Online' : 'Away'}</span>}
                <span className="score">{p.score}</span>
              </div>
            </div>
          ))}
          {room.phase === 'lobby' && (
            isHost
              ? <div className="stack-actions">
                  {neededPlayers > 0
                    ? <button className="btn primary block" onClick={() => send('start_computer_game')}>Start with computer</button>
                    : <button className="btn primary block" onClick={() => send('start_game')}>Start game</button>}
                  <button className="btn ghost block small" onClick={shareRoom}>Invite friends</button>
                </div>
              : <p className="muted small">Waiting for the host to start...</p>
          )}
          {isHost && room.phase !== 'lobby' && room.phase !== 'finished' && (
            <button className="btn ghost block small" onClick={() => send('end_game')}>End game</button>
          )}
        </aside>

        <div className="stage card">
          {room.phase === 'lobby' && (
            <LobbyStage
              room={room}
              isHost={isHost}
              neededPlayers={neededPlayers}
              shareRoom={shareRoom}
              copyLink={copyLink}
              send={send}
            />
          )}

          {room.phase === 'finished' && <Finished room={room} isHost={isHost} send={send} />}

          {(room.phase === 'submitting' || room.phase === 'revealing' || room.phase === 'judging' || room.phase === 'scoring') && (
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
                <ReportControl key={room.prompt?.uuid || room.prompt?.id} card={room.prompt} kind="prompt" roomCode={room.code} session={session} />
              </div>
              {room.phase === 'scoring' && winner && (
                <div className="winner-spotlight" aria-live="polite">
                  <span className="eyebrow">Winner</span>
                  <p className="winner-answer">{winner.answer?.text}</p>
                  <p className="winner-by"><b>{winner.player_name}</b> takes the point</p>
                  <ReportControl key={winner.answer?.uuid || winner.answer?.id} card={winner.answer} kind="answer" roomCode={room.code} session={session} />
                </div>
              )}
              <div className="submissions">
                {winner && others.length > 0 && <p className="muted small also-played">Also played</p>}
                {others.map((s) => (
                  <div className={submissionClass(s, room.phase)} key={s.id}>
                    <span>{cardFaceText(s, room.phase)}</span>
                    {isJudge && room.phase === 'judging' && (
                      <button className="btn small" onClick={() => sendOnce('pick_winner', { submission_id: s.id })}>Pick</button>
                    )}
                    {room.phase === 'scoring' && s.player_name && <span className="by">{s.player_name}</span>}
                    {s.revealed && <ReportControl key={s.answer?.uuid || s.answer?.id} card={s.answer} kind="answer" roomCode={room.code} session={session} />}
                  </div>
                ))}
                {room.phase === 'submitting' && (room.submissions || []).length === 0 && <p className="muted">No answers in yet...</p>}
                {room.phase === 'scoring' && (room.submissions || []).length === 0 && <p className="muted">Nobody answered in time. On to the next one.</p>}
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
                        onClick={() => sendOnce('submit_answer', { answer_card_id: c.id })}>
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

function LobbyStage({ room, isHost, neededPlayers, shareRoom, copyLink, send }) {
  const previewSeats = [...room.players];
  while (previewSeats.length < Math.min(room.max_players, 4)) previewSeats.push(null);
  const computers = room.players.filter((p) => p.is_computer).length;

  return (
    <div className="lobby-stage">
      <div className="lobby-copy">
        <span className="eyebrow">{computers > 0 ? 'Computer room' : 'Live room'}</span>
        <h2>{isHost ? 'Choose how this table starts' : 'You are at the table'}</h2>
        <p>
          {isHost
            ? neededPlayers > 0
              ? 'Practice now with computer players, or send the room code when friends are ready.'
              : 'The table has enough players. Start whenever the room is settled.'
            : 'Keep this tab open. The host starts the first prompt.'}
        </p>
      </div>

      <div className="table-preview" aria-label="Room seats">
        <button className="table-code" onClick={copyLink} title="Copy invite link">
          <span>Room</span>
          <b>{room.code}</b>
        </button>
        <div className="seat-grid">
          {previewSeats.map((player, index) => (
            <div className={`seat ${player ? 'filled' : 'open'} ${player?.is_computer ? 'computer' : ''}`} key={player?.id || `open-${index}`}>
              <span className="seat-index">{index + 1}</span>
              <b>{player?.name || 'Open seat'}</b>
              <small>{player ? (player.is_computer ? 'Computer' : player.id === room.host_id ? 'Host' : 'Player') : 'Invite or fill'}</small>
            </div>
          ))}
        </div>
      </div>

      {isHost ? (
        <div className="lobby-actions">
          {neededPlayers > 0
            ? <button className="btn primary" onClick={() => send('start_computer_game')}>Play with computer</button>
            : <button className="btn primary" onClick={() => send('start_game')}>Start game</button>}
          <button className="btn" onClick={shareRoom}>Invite friends</button>
          <button className="btn ghost" onClick={copyLink}>Copy link</button>
        </div>
      ) : (
        <p className="muted small">Waiting for the host to pick the start.</p>
      )}

      <RoomSettings room={room} isHost={isHost} send={send} />
    </div>
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

  if (room.phase === 'revealing') {
    const total = (room.submissions || []).length;
    const shown = room.reveal_index || 0;
    title = 'Answers are coming up';
    body = isJudge
      ? `Card ${Math.min(shown, total)} of ${total}. You can pick once every answer is face up.`
      : `Card ${Math.min(shown, total)} of ${total}. Everyone sees them at the same time.`;
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
      {player.is_computer && <span className="role-badge bot-badge">CPU</span>}
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
  if (player.is_computer) return player.submitted ? 'In' : 'Thinking';
  return player.submitted ? 'In' : 'Choosing';
}

function connectionLabel(status) {
  return { idle: 'Idle', connecting: 'Connecting', connected: 'Live', closed: 'Reconnecting' }[status] || status;
}

// The server accepts wider ranges than these presets (score 1-20, timer
// 15-300s, players 3-20), and computer rooms are created at 45s. A <select>
// whose value matches no option silently displays the first one, so the host
// was shown "30s" while the room really ran a 45s timer. Any value off the
// preset list is added to it instead.
function withCurrent(options, current) {
  if (current == null || options.includes(current)) return options;
  return [...options, current].sort((a, b) => a - b);
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
            {withCurrent([3, 5, 7, 10], room.score_limit).map((n) => <option key={n} value={n}>{n} points</option>)}
          </select>
        </label>
        <label>Answer timer
          <select value={room.round_seconds} onChange={(e) => update({ round_seconds: +e.target.value })}>
            {withCurrent([30, 60, 90, 120], room.round_seconds).map((n) => <option key={n} value={n}>{n}s</option>)}
          </select>
        </label>
        <label>Max players
          <select value={room.max_players} onChange={(e) => update({ max_players: +e.target.value })}>
            {withCurrent([4, 6, 8, 12], room.max_players).map((n) => <option key={n} value={n} disabled={n < playerCount}>{n}</option>)}
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
  return { lobby: 'Lobby', submitting: 'Answering', revealing: 'Reveal', judging: 'Judging', scoring: 'Winner', finished: 'Game over' }[phase] || phase;
}

const DAILY_SESSIONS_KEY = 'punchline.daily.sessions';
const DAILY_ALPHABET = '23456789ABCDEFGHJKMNPQRSTUVWXYZ';
const loadDailySessions = () => {
  try {
    const value = JSON.parse(localStorage.getItem(DAILY_SESSIONS_KEY) || '[]');
    return Array.isArray(value) ? value.filter((item) => item?.code && item?.token) : [];
  } catch { return []; }
};
const saveDailySessions = (sessions) => localStorage.setItem(DAILY_SESSIONS_KEY, JSON.stringify(sessions));
const normalizeDailyCode = (value) => [...String(value || '').toUpperCase()]
  .filter((character) => DAILY_ALPHABET.includes(character)).slice(0, 6).join('');

function rememberDailySession(session, sessions) {
  const normalized = {
    code: normalizeDailyCode(session.group.code),
    token: session.token,
    groupName: session.group.name,
    playerName: session.membership.display_name,
  };
  const next = [normalized, ...sessions.filter((item) => item.code !== normalized.code)].slice(0, 20);
  saveDailySessions(next);
  return { normalized, next };
}

async function dailyRequest(path, { method = 'GET', token, body } = {}) {
  const headers = {};
  if (body) headers['Content-Type'] = 'application/json';
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${API}${path}`, { method, headers, body: body ? JSON.stringify(body) : undefined });
  const data = await readPayload(res);
  if (!res.ok) throw new Error(data.error || 'The daily request failed.');
  return data;
}

function DailyApp() {
  // Daily groups never expire, so a double-tapped create leaves a permanent
  // orphan group that the daily worker generates a round for every day.
  const inFlight = useRef(false);
  const joinCode = normalizeDailyCode(new URLSearchParams(location.search).get('join') || '');
  const initialSessions = loadDailySessions();
  const initialActive = joinCode ? initialSessions.find((item) => item.code === joinCode) : initialSessions[0];
  const [sessions, setSessions] = useState(initialSessions);
  const [active, setActive] = useState(initialActive || null);
  const [view, setView] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');

  const loadToday = useCallback(async (session = active, quiet = false) => {
    if (!session) return;
    if (!quiet) setBusy('load');
    try {
      const data = await dailyRequest(`/api/daily/groups/${session.code}/today`, { token: session.token });
      setView(data);
      setError('');
    } catch (err) {
      setError(errorMessage(err, 'Could not load the daily round.'));
    } finally {
      if (!quiet) setBusy('');
    }
  }, [active]);

  useEffect(() => {
    if (active) loadToday(active);
  }, [active, loadToday]);

  useEffect(() => {
    if (!active) return undefined;
    const id = setInterval(() => loadToday(active, true), 60000);
    return () => clearInterval(id);
  }, [active, loadToday]);

  function activateSession(sessionResponse) {
    const remembered = rememberDailySession(sessionResponse, sessions);
    setSessions(remembered.next);
    setActive(remembered.normalized);
    setView(null);
  }

  async function createGroup(input) {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy('create'); setError('');
    try {
      const data = await dailyRequest('/api/daily/groups', { method: 'POST', body: input });
      activateSession(data);
      history.replaceState(null, '', '/daily');
    } catch (err) { setError(errorMessage(err, 'Could not create the group.')); }
    finally { inFlight.current = false; setBusy(''); }
  }

  async function joinGroup(code, playerName) {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy('join'); setError('');
    try {
      const normalized = normalizeDailyCode(code);
      const data = await dailyRequest(`/api/daily/groups/${normalized}/join`, {
        method: 'POST', body: { player_name: playerName },
      });
      activateSession(data);
      history.replaceState(null, '', '/daily');
    } catch (err) { setError(errorMessage(err, 'Could not join the group.')); }
    finally { inFlight.current = false; setBusy(''); }
  }

  function switchGroup(session) {
    setView(null); setError(''); setActive(session);
    history.replaceState(null, '', '/daily');
  }

  if (!active || (joinCode && active.code !== joinCode)) {
    return <DailyLanding sessions={sessions} joinCode={joinCode} busy={busy} error={error}
      onCreate={createGroup} onJoin={joinGroup} onOpen={switchGroup} />;
  }
  return <DailyDashboard session={active} sessions={sessions} view={view} busy={busy} error={error}
    onRefresh={() => loadToday(active)} onSwitch={() => { setActive(null); setView(null); }} />;
}

function DailyLanding({ sessions, joinCode, busy, error, onCreate, onJoin, onOpen }) {
  const [playerName, setPlayerName] = useState(localStorage.getItem('punchline.name') || '');
  const [groupName, setGroupName] = useState('');
  const [code, setCode] = useState(joinCode);
  // Group codes never contain O, 0, 1, I or L, so those characters are dropped
  // as you type. Silently eating them leaves a code that looks complete beside
  // a Join button that refuses to light up, with nothing explaining why.
  const [codeNotice, setCodeNotice] = useState('');
  const typeCode = (value) => {
    const cleaned = normalizeDailyCode(value);
    const dropped = [...value.toUpperCase()].some((c) => /[A-Z0-9]/.test(c) && !DAILY_ALPHABET.includes(c));
    setCodeNotice(dropped ? 'Codes skip O, 0, 1, I and L to keep them easy to read aloud.' : '');
    setCode(cleaned);
  };
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  const rememberName = (value) => { setPlayerName(value); localStorage.setItem('punchline.name', value); };
  const cleanPlayer = playerName.trim();

  return (
    <main className="shell daily-shell">
      <header className="daily-topbar">
        <a className="logo daily-logo" href="/">Punchline</a>
        <a className="btn ghost small" href="/">Live game</a>
      </header>
      <section className="daily-hero">
        <div>
          <span className="eyebrow">One prompt. All day.</span>
          <h1>Keep the group chat funny between game nights.</h1>
          <p>Submit before the reveal, vote blind, then see who won. Your timezone is used so the daily deadline makes sense.</p>
        </div>
        <div className="panel daily-start-panel" aria-busy={!!busy}>
          <label htmlFor="daily-player">Your name</label>
          <input id="daily-player" className="field" maxLength={20} value={playerName} autoComplete="nickname"
            onChange={(event) => rememberName(event.target.value)} placeholder="e.g. Sam" />
          {joinCode ? <>
            <div className="daily-invite-code">Invited to <b>{joinCode}</b></div>
            <button className="btn primary block" disabled={!cleanPlayer || !!busy} onClick={() => onJoin(joinCode, cleanPlayer)}>
              {busy === 'join' ? 'Joining…' : 'Join daily group'}
            </button>
          </> : <>
            <label htmlFor="daily-group">New group name</label>
            <input id="daily-group" className="field" maxLength={40} value={groupName}
              onChange={(event) => setGroupName(event.target.value)} placeholder="Friday people" />
            <button className="btn primary block" disabled={!cleanPlayer || groupName.trim().length < 2 || !!busy}
              onClick={() => onCreate({ name: groupName.trim(), player_name: cleanPlayer, timezone })}>
              {busy === 'create' ? 'Creating…' : 'Create daily group'}
            </button>
            <div className="divider"><span>or join with a code</span></div>
            <div className="join-box daily-join-box">
              <input className="field code-input" aria-label="Daily group code" maxLength={6} value={code}
                onChange={(event) => typeCode(event.target.value)} placeholder="6-letter code" />
              <button className="btn" disabled={!cleanPlayer || code.length !== 6 || !!busy} onClick={() => onJoin(code, cleanPlayer)}>
                {busy === 'join' ? 'Joining…' : 'Join'}
              </button>
            </div>
            {codeNotice && <p className="muted small" aria-live="polite">{codeNotice}</p>}
          </>}
          {error && <p className="err" role="alert">{error}</p>}
        </div>
      </section>
      {sessions.length > 0 && !joinCode && <section className="saved-groups" aria-label="Your saved daily groups">
        <h2>Your groups</h2>
        <div className="saved-group-grid">
          {sessions.map((session) => <button className="saved-group" key={session.code} onClick={() => onOpen(session)}>
            <b>{session.groupName}</b><span>{session.code} · as {session.playerName}</span>
          </button>)}
        </div>
      </section>}
    </main>
  );
}

function DailyDashboard({ session, sessions, view, busy, error, onRefresh, onSwitch }) {
  const inviteURL = `${location.origin}/daily?join=${session.code}`;
  const [notice, setNotice] = useState('');

  async function shareInvite() {
    const text = `Join ${view?.group?.name || session.groupName} on Punchline Daily.`;
    try {
      if (navigator.share) await navigator.share({ title: 'Punchline Daily', text, url: inviteURL });
      else { await navigator.clipboard.writeText(inviteURL); setNotice('Invite link copied.'); }
    } catch (err) {
      if (err?.name !== 'AbortError') setNotice(`Copy this invite: ${inviteURL}`);
    }
  }

  async function deleteGroup() {
    if (!confirm(`Delete ${view.group.name} and every daily round? This cannot be undone.`)) return;
    try {
      await dailyRequest(`/api/daily/groups/${session.code}`, { method: 'DELETE', token: session.token });
      const next = sessions.filter((item) => item.code !== session.code);
      saveDailySessions(next);
      location.href = '/daily';
    } catch (err) {
      setNotice(errorMessage(err, 'Could not delete the group.'));
    }
  }

  return (
    <main className="shell daily-shell">
      <header className="daily-topbar">
        <a className="logo daily-logo" href="/">Punchline</a>
        <div className="daily-nav-actions">
          <button className="btn small" onClick={shareInvite}>Invite</button>
          <button className="btn ghost small" onClick={onSwitch}>{sessions.length > 1 ? 'Switch group' : 'Groups'}</button>
        </div>
      </header>
      {error && <p className="err banner" role="alert">{error} <button className="inline-button" onClick={onRefresh}>Try again</button></p>}
      {notice && <p className="share-notice" aria-live="polite">{notice}</p>}
      {!view ? <section className="daily-loading"><div className="logo daily-logo">Punchline</div><p>{busy ? 'Loading today’s prompt…' : 'Getting the round ready…'}</p></section>
        : <><DailyRoundView view={view} session={session} onChanged={onRefresh} setNotice={setNotice} />
          {view.membership.role === 'owner' && <div className="daily-danger"><button className="inline-button" onClick={deleteGroup}>Delete this daily group</button></div>}</>}
    </main>
  );
}

function DailyRoundView({ view, session, onChanged, setNotice }) {
  const round = view.today;
  const previous = view.previous;
  // A finalized round has no deadline left to count down to; it kept rendering
  // "Voting closes in Updating..." forever once the result landed.
  const deadline = round.state === 'OPEN_FOR_SUBMISSIONS' ? round.reveal_at
    : round.state === 'REVEALED_FOR_VOTING' ? round.voting_closes_at : null;
  const [answer, setAnswer] = useState(round.my_submission?.answer_text || '');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');

  useEffect(() => setAnswer(round.my_submission?.answer_text || ''), [round.id, round.my_submission?.answer_text]);

  async function submit() {
    setBusy('submit'); setError('');
    try {
      await dailyRequest(`/api/daily/rounds/${round.id}/submit`, {
        method: 'POST', token: session.token, body: { answer_text: answer.trim() },
      });
      setNotice(round.my_submission ? 'Your answer was updated.' : 'Your answer is locked in.');
      await onChanged();
    } catch (err) { setError(errorMessage(err, 'Could not save your answer.')); }
    finally { setBusy(''); }
  }

  async function vote(submissionID) {
    setBusy(`vote:${submissionID}`); setError('');
    try {
      await dailyRequest(`/api/daily/rounds/${round.id}/vote`, {
        method: 'POST', token: session.token, body: { submission_id: submissionID },
      });
      setNotice(round.my_vote_submission_id ? 'Your vote was changed.' : 'Vote counted.');
      await onChanged();
    } catch (err) { setError(errorMessage(err, 'Could not save your vote.')); }
    finally { setBusy(''); }
  }

  return <>
    <section className="daily-heading">
      <div>
        <span className="eyebrow">{view.group.name}</span>
        <h1>Daily #{round.number}</h1>
        <p className="muted">Playing as {view.membership.display_name} · {view.group.member_count} members</p>
      </div>
      <div className="daily-streak"><b>{view.streak}</b><span>day streak</span></div>
    </section>
    <section className="daily-grid">
      <article className="daily-main card">
        <div className="daily-round-meta">
          <span className={`phase-chip daily-state ${round.state.toLowerCase()}`}>{dailyStateLabel(round.state)}</span>
          {deadline && <Deadline deadline={deadline} prefix={round.state === 'OPEN_FOR_SUBMISSIONS' ? 'Reveal' : 'Voting closes'} />}
          {!deadline && <span className="daily-deadline">Next prompt at local midnight</span>}
        </div>
        <div className="prompt-card daily-prompt"><span className="prompt-label">Today’s prompt</span><p>{round.prompt}</p></div>
        {round.state === 'OPEN_FOR_SUBMISSIONS' && <div className="daily-compose">
          <label htmlFor="daily-answer">Your punchline</label>
          <textarea id="daily-answer" className="field" maxLength={160} rows={4} value={answer}
            onChange={(event) => setAnswer(event.target.value)} placeholder="Write the line that makes the group lose it…" />
          <div className="compose-footer"><span>{answer.length}/160</span>
            <button className="btn primary" disabled={!answer.trim() || !!busy} onClick={submit}>
              {busy === 'submit' ? 'Saving…' : round.my_submission ? 'Update answer' : 'Lock in answer'}
            </button>
          </div>
          <p className="muted small">Answers stay private until reveal. You can edit yours before the deadline.</p>
        </div>}
        {round.state === 'REVEALED_FOR_VOTING' && <DailyVoting round={round} busy={busy} onVote={vote} />}
        {round.state === 'FINALIZED' && <DailyResults round={round} streak={view.streak} groupName={view.group.name} setNotice={setNotice} />}
        {error && <p className="err" role="alert">{error}</p>}
      </article>
      <aside className="daily-side">
        <section className="card daily-progress">
          <h3>Group progress</h3>
          <b>{round.submission_count || 0}/{round.member_count || 0}</b>
          <span>answers submitted</span>
          <div className="progress-track"><span style={{ width: `${Math.min(100, ((round.submission_count || 0) / Math.max(1, round.member_count || 0)) * 100)}%` }} /></div>
        </section>
        {previous && <section className="card previous-round">
          <h3>Previous round</h3>
          <p>{previous.prompt}</p>
          {previous.state === 'FINALIZED' ? <DailyResults round={previous} streak={view.streak} groupName={view.group.name} compact setNotice={setNotice} />
            : <span className="muted small">Results are still being decided.</span>}
        </section>}
        <section className="card daily-rules"><h3>How it works</h3><ol><li>Submit before reveal.</li><li>Authors stay hidden while voting.</li><li>Most votes wins; ties share the crown.</li></ol></section>
      </aside>
    </section>
  </>;
}

function DailyVoting({ round, busy, onVote }) {
  if (!(round.submissions || []).length) return <div className="daily-empty"><h2>No answers today</h2><p>The next prompt opens at local midnight.</p></div>;
  return <section className="daily-voting"><h2>Vote for the funniest</h2><p className="muted">Authors stay hidden until the result. You can change your vote before close.</p>
    <div className="daily-submissions">{round.submissions.map((submission) => <button key={submission.id}
      className={`daily-submission ${submission.my_vote ? 'selected' : ''}`} disabled={submission.mine || !!busy}
      onClick={() => onVote(submission.id)}>
      <span>{submission.answer_text}</span><small>{submission.mine ? 'Your answer' : submission.my_vote ? 'Your vote' : 'Vote'}</small>
    </button>)}</div>
  </section>;
}

function DailyResults({ round, streak, groupName, compact = false, setNotice }) {
  const winners = (round.submissions || []).filter((item) => item.winner);
  if (!(round.submissions || []).length) return <p className="muted">No answers were submitted.</p>;
  return <section className={`daily-results ${compact ? 'compact' : ''}`}>
    {!compact && <><h2>{winners.length ? (winners.length > 1 ? 'It’s a tie' : `${winners[0].author_name} wins`) : 'No votes this round'}</h2>
      <p className="muted">The group has spoken.</p></>}
    <div className="result-list">{round.submissions.map((submission) => <div className={`result-row ${submission.winner ? 'winner' : ''}`} key={submission.id}>
      <span><b>{submission.answer_text}</b><small>{submission.author_name}</small></span>
      <strong>{plural(submission.vote_count || 0, 'vote')}</strong>
    </div>)}</div>
    <button className="btn ghost block" onClick={() => downloadDailyShareCard(round, streak, groupName, setNotice)}>Download spoiler-safe share card</button>
  </section>;
}

function Deadline({ deadline, prefix }) {
  const seconds = useCountdown(deadline);
  const label = seconds <= 0 ? 'Updating…' : formatDuration(seconds);
  return <span className="daily-deadline">{prefix} in <b>{label}</b></span>;
}

function formatDuration(seconds) {
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours >= 24) return `${Math.floor(hours / 24)}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${Math.max(1, minutes)}m`;
}

function dailyStateLabel(state) {
  return { OPEN_FOR_SUBMISSIONS: 'Submissions open', REVEALED_FOR_VOTING: 'Voting open', FINALIZED: 'Final result' }[state] || state;
}

function downloadDailyShareCard(round, streak, groupName, setNotice) {
  const winners = (round.submissions || []).filter((item) => item.winner).map((item) => item.author_name);
  const winnerText = winners.length ? winners.join(' + ') : 'No winner';
  const canvas = document.createElement('canvas');
  canvas.width = 1200; canvas.height = 1200;
  const context = canvas.getContext('2d');
  const gradient = context.createLinearGradient(0, 0, 1200, 1200);
  gradient.addColorStop(0, '#ff6b4a'); gradient.addColorStop(1, '#20c7a8');
  context.fillStyle = '#101114'; context.fillRect(0, 0, 1200, 1200);
  context.fillStyle = gradient; context.fillRect(70, 70, 1060, 14);
  context.fillStyle = '#f6c85f'; context.font = '700 34px system-ui'; context.fillText(groupName.toUpperCase(), 90, 180);
  context.fillStyle = '#f4f4f7'; context.font = '800 104px system-ui'; context.fillText(`DAILY #${round.number}`, 90, 330);
  context.fillStyle = '#a6abb7'; context.font = '600 42px system-ui'; context.fillText('WINNER', 90, 460);
  context.fillStyle = '#f4f4f7'; context.font = '800 76px system-ui'; wrapCanvasText(context, winnerText, 90, 565, 980, 90);
  context.fillStyle = '#20c7a8'; context.font = '800 56px system-ui'; context.fillText(`${streak} DAY GROUP STREAK`, 90, 900);
  context.fillStyle = '#a6abb7'; context.font = '600 36px system-ui'; context.fillText('Answers hidden. Come play tomorrow.', 90, 990);
  context.fillStyle = gradient; context.font = '800 52px system-ui'; context.fillText('PUNCHLINE', 90, 1090);
  const link = document.createElement('a');
  link.download = `punchline-daily-${round.number}.png`; link.href = canvas.toDataURL('image/png'); link.click();
  setNotice('Spoiler-safe share card downloaded.');
}

function wrapCanvasText(context, text, x, y, maxWidth, lineHeight) {
  const words = text.split(' '); let line = '';
  for (const word of words) {
    const test = `${line}${word} `;
    if (context.measureText(test).width > maxWidth && line) { context.fillText(line.trim(), x, y); line = `${word} `; y += lineHeight; }
    else line = test;
  }
  context.fillText(line.trim(), x, y);
}


// --- Reporting -------------------------------------------------------------

const REPORT_REASONS = [
  ['offensive', 'Offensive'],
  ['broken', 'Broken or confusing'],
  ['unfunny', 'Just not funny'],
  ['duplicate', 'Duplicate'],
];

// Cards only carry a uuid when they came from the database, so the control
// hides itself for seed-backed decks rather than offering a dead button.
function ReportControl({ card, kind, roomCode, session }) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState('');
  if (!card?.uuid) return null;

  async function report(reason) {
    setState('sending');
    try {
      const res = await fetch('/api/cards/report', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          card_kind: kind, card_id: card.uuid, reason, detail: '',
          room_code: roomCode || '',
          // Identifies the reporter as a person in this room rather than as an
          // address, so a table full of friends on one WiFi counts as a table
          // full of reporters.
          player_id: session?.playerId || '', token: session?.token || '',
        }),
      });
      if (!res.ok) throw new Error('report failed');
      setState('done');
      setOpen(false);
    } catch {
      setState('failed');
    }
  }

  if (state === 'done') return <span className="report-done" role="status">Reported. Thanks.</span>;
  return (
    <div className="report-control">
      <button className="inline-button subtle" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        {open ? 'Cancel' : 'Report card'}
      </button>
      {open && <div className="report-reasons">
        {REPORT_REASONS.map(([value, label]) => (
          <button key={value} className="btn small ghost" disabled={state === 'sending'} onClick={() => report(value)}>{label}</button>
        ))}
      </div>}
      {state === 'failed' && <span className="err small">Could not send that report.</span>}
    </div>
  );
}

// --- Admin desk ------------------------------------------------------------

const ADMIN_TOKEN_KEY = 'punchline.admin.token';

async function adminRequest(path, { method = 'GET', token, body } = {}) {
  const headers = { Authorization: `Bearer ${token}` };
  if (body) headers['Content-Type'] = 'application/json';
  const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
  if (res.status === 401) throw new Error('That admin token was rejected.');
  if (res.status === 404) throw new Error('The admin desk is not enabled on this deployment.');
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(data.error || 'The admin request failed.');
  return data;
}

function AdminApp() {
  const [token, setToken] = useState(() => localStorage.getItem(ADMIN_TOKEN_KEY) || '');
  const [authed, setAuthed] = useState(false);
  const [overview, setOverview] = useState(null);
  const [error, setError] = useState('');
  const [tab, setTab] = useState('reports');

  const refreshOverview = useCallback(async (activeToken) => {
    const data = await adminRequest('/api/admin/overview', { token: activeToken });
    setOverview(data);
    setAuthed(true);
  }, []);

  useEffect(() => {
    if (!token) return;
    refreshOverview(token).catch((err) => { setError(errorMessage(err, 'Could not load the admin desk.')); setAuthed(false); });
  }, [token, refreshOverview]);

  async function signIn(candidate) {
    setError('');
    try {
      await refreshOverview(candidate);
      localStorage.setItem(ADMIN_TOKEN_KEY, candidate);
      setToken(candidate);
    } catch (err) {
      setError(errorMessage(err, 'Could not sign in.'));
    }
  }

  function signOut() {
    localStorage.removeItem(ADMIN_TOKEN_KEY);
    setToken('');
    setAuthed(false);
    setOverview(null);
  }

  if (!authed) return <AdminSignIn onSignIn={signIn} error={error} initial={token} />;

  return (
    <main className="shell admin-shell">
      <header className="daily-topbar">
        <a className="logo daily-logo" href="/">Punchline</a>
        <div className="daily-nav-actions">
          <span className="admin-badge">Admin desk</span>
          <button className="btn ghost small" onClick={signOut}>Sign out</button>
        </div>
      </header>

      {overview && <section className="admin-stats">
        <AdminStat label="Open reports" value={overview.open_reports} accent={overview.open_reports > 0} />
        <AdminStat label="Pending candidates" value={overview.pending_candidates} accent={overview.pending_candidates > 0} />
        <AdminStat label="Live prompts" value={overview.approved_prompts} />
        <AdminStat label="Live answers" value={overview.approved_answers} />
        <AdminStat label="Retired" value={overview.retired_cards} />
        <AdminStat label="Promoted from daily" value={overview.community_promoted} />
        <AdminStat label="Daily groups" value={overview.daily_groups} />
        <AdminStat label="Active members (7d)" value={overview.active_daily_members} />
      </section>}

      <nav className="admin-tabs" role="tablist">
        {[['reports', 'Reports'], ['candidates', 'Candidates'], ['cards', 'Cards']].map(([id, label]) => (
          <button key={id} role="tab" aria-selected={tab === id}
            className={`btn small ${tab === id ? 'primary' : 'ghost'}`} onClick={() => setTab(id)}>{label}</button>
        ))}
      </nav>

      {error && <p className="err banner" role="alert">{error}</p>}

      {tab === 'reports' && <AdminReports token={token} onChanged={() => refreshOverview(token)} autoRetireAt={overview?.auto_retire_at} />}
      {tab === 'candidates' && <AdminCandidates token={token} onChanged={() => refreshOverview(token)} />}
      {tab === 'cards' && <AdminCards token={token} onChanged={() => refreshOverview(token)} />}
    </main>
  );
}

function AdminSignIn({ onSignIn, error, initial }) {
  const [value, setValue] = useState(initial || '');
  return (
    <main className="shell admin-shell">
      <section className="panel admin-signin">
        <h1>Admin desk</h1>
        <p className="muted">Enter the deployment's admin token. It stays in this browser.</p>
        <label htmlFor="admin-token">Admin token</label>
        <input id="admin-token" className="field" type="password" value={value} autoComplete="off"
          onChange={(event) => setValue(event.target.value)} />
        <button className="btn primary block" disabled={!value.trim()} onClick={() => onSignIn(value.trim())}>Open the desk</button>
        {error && <p className="err" role="alert">{error}</p>}
      </section>
    </main>
  );
}

function AdminStat({ label, value, accent = false }) {
  return <div className={`admin-stat ${accent ? 'accent' : ''}`}><b>{value ?? '—'}</b><span>{label}</span></div>;
}

function useAdminList(path, token, deps = []) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const latest = useRef(0);
  const alive = useRef(true);
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  // Changing a filter leaves the previous request in flight. Only the newest
  // one may write state, or a slow response repaints the list for a filter the
  // admin already left — and an empty stale list reads as "no such card".
  const load = useCallback(async () => {
    const id = ++latest.current;
    const current = () => alive.current && id === latest.current;
    setBusy(true); setError('');
    try {
      const next = await adminRequest(path, { token });
      if (current()) setData(next);
    } catch (err) {
      if (current()) setError(errorMessage(err, 'Could not load that queue.'));
    } finally {
      if (current()) setBusy(false);
    }
  }, [path, token]);
  useEffect(() => { load(); }, [load, ...deps]);
  return { data, error, busy, reload: load };
}

function AdminReports({ token, onChanged, autoRetireAt }) {
  const { data, error, busy, reload } = useAdminList('/api/admin/reports?limit=100', token);
  const [acting, setActing] = useState('');

  async function resolve(report, resolution) {
    setActing(report.card_id);
    try {
      await adminRequest('/api/admin/reports', {
        method: 'POST', token,
        body: { card_kind: report.card_kind, card_id: report.card_id, resolution },
      });
      await reload();
      await onChanged();
    } catch (err) {
      alert(errorMessage(err, 'Could not resolve that report.'));
    } finally { setActing(''); }
  }

  const reports = data?.reports || [];
  return (
    <section className="admin-queue">
      <div className="admin-queue-head">
        <h2>Open reports</h2>
        <span className="muted small">Cards are retired automatically at {autoRetireAt ?? 3} distinct reporters.</span>
      </div>
      {error && <p className="err" role="alert">{error}</p>}
      {busy && !data && <p className="muted">Loading…</p>}
      {!busy && reports.length === 0 && <p className="muted">Nothing reported. The deck is behaving.</p>}
      <div className="admin-rows">
        {reports.map((report) => (
          <article className="admin-row" key={report.id}>
            <div className="admin-row-main">
              <span className={`tag ${report.card_kind}`}>{report.card_kind}</span>
              <p>{report.card_text}</p>
              <small className="muted">
                {report.reason}{report.detail ? ` — ${report.detail}` : ''} · {report.reports_for_card} reporter(s)
                {report.room_code ? ` · room ${report.room_code}` : ''} · currently {report.card_status}
              </small>
            </div>
            <div className="admin-row-actions">
              <button className="btn small" disabled={acting === report.card_id} onClick={() => resolve(report, 'retired')}>Retire card</button>
              <button className="btn small ghost" disabled={acting === report.card_id} onClick={() => resolve(report, 'dismissed')}>Dismiss</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function AdminCandidates({ token, onChanged }) {
  const { data, error, busy, reload } = useAdminList('/api/admin/candidates?limit=100', token);
  const [acting, setActing] = useState('');

  async function review(candidate, decision, tier = 'party') {
    setActing(candidate.id);
    try {
      await adminRequest('/api/admin/candidates', {
        method: 'POST', token, body: { candidate_id: candidate.id, decision, tier },
      });
      await reload();
      await onChanged();
    } catch (err) {
      alert(errorMessage(err, 'Could not review that candidate.'));
    } finally { setActing(''); }
  }

  const candidates = data?.candidates || [];
  return (
    <section className="admin-queue">
      <div className="admin-queue-head">
        <h2>Candidates from daily play</h2>
        <span className="muted small">Answers players wrote and voted for. Approving adds the card to the community pack.</span>
      </div>
      {error && <p className="err" role="alert">{error}</p>}
      {busy && !data && <p className="muted">Loading…</p>}
      {!busy && candidates.length === 0 && <p className="muted">No candidates waiting. They arrive as daily rounds finalize.</p>}
      <div className="admin-rows">
        {candidates.map((candidate) => (
          <article className="admin-row" key={candidate.id}>
            <div className="admin-row-main">
              <p>{candidate.text}</p>
              <small className="muted">{candidate.vote_count} votes · written by {candidate.author_name || 'unknown'}</small>
            </div>
            <div className="admin-row-actions">
              <button className="btn small primary" disabled={acting === candidate.id} onClick={() => review(candidate, 'approved', 'party')}>Approve</button>
              <button className="btn small" disabled={acting === candidate.id} onClick={() => review(candidate, 'approved', 'family')}>Approve as family</button>
              <button className="btn small ghost" disabled={acting === candidate.id} onClick={() => review(candidate, 'rejected')}>Reject</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

// Prompts and answers count different things, so each kind gets its own lenses:
// only prompts track skips, only answers track wins.
const CARD_SORTS = {
  answer: [['played', 'Most played'], ['unplayed', 'Least played'], ['winning', 'Most wins'], ['reported', 'Most reported']],
  prompt: [['played', 'Most played'], ['unplayed', 'Least played'], ['skipped', 'Most skipped'], ['reported', 'Most reported']],
};

function AdminCards({ token, onChanged }) {
  const [kind, setKind] = useState('answer');
  const [sort, setSort] = useState('played');
  const [status, setStatus] = useState('approved');
  const path = `/api/admin/cards?kind=${kind}&sort=${sort}&status=${status}&limit=100`;
  const { data, error, busy, reload } = useAdminList(path, token, [kind, sort, status]);
  const [acting, setActing] = useState('');

  async function setCardStatus(card, next) {
    setActing(card.id);
    try {
      await adminRequest('/api/admin/cards', {
        method: 'POST', token, body: { card_kind: card.kind, card_id: card.id, status: next },
      });
      await reload();
      await onChanged();
    } catch (err) {
      alert(errorMessage(err, 'Could not update that card.'));
    } finally { setActing(''); }
  }

  function changeKind(next) {
    setKind(next);
    if (!CARD_SORTS[next].some(([value]) => value === sort)) setSort('played');
  }

  const cards = data?.cards || [];
  const rate = (card) => (card.kind === 'answer'
    ? `${Math.round((card.win_rate || 0) * 100)}% win`
    : `${Math.round((card.skip_rate || 0) * 100)}% skip`);

  return (
    <section className="admin-queue">
      <div className="admin-queue-head">
        <h2>Cards</h2>
        <div className="admin-filters">
          <select className="field small" value={kind} onChange={(e) => changeKind(e.target.value)} aria-label="Card kind">
            <option value="answer">Answers</option>
            <option value="prompt">Prompts</option>
          </select>
          <select className="field small" value={sort} onChange={(e) => setSort(e.target.value)} aria-label="Sort by">
            {CARD_SORTS[kind].map(([value, label]) => <option key={value} value={value}>{label}</option>)}
          </select>
          <select className="field small" value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Status">
            <option value="approved">Live</option>
            <option value="retired">Retired</option>
          </select>
        </div>
      </div>
      {error && <p className="err" role="alert">{error}</p>}
      {busy && !data && <p className="muted">Loading…</p>}
      {!busy && cards.length === 0 && <p className="muted">No cards match that filter.</p>}
      <div className="admin-rows">
        {cards.map((card) => (
          <article className="admin-row" key={card.id}>
            <div className="admin-row-main">
              <p>{card.text}</p>
              <small className="muted">
                {card.pack} · {card.tier} · played {card.times_played}× · {rate(card)}
                {card.report_count > 0 ? ` · ${card.report_count} report(s)` : ''}
              </small>
            </div>
            <div className="admin-row-actions">
              {card.status === 'approved'
                ? <button className="btn small" disabled={acting === card.id} onClick={() => setCardStatus(card, 'retired')}>Retire</button>
                : <button className="btn small primary" disabled={acting === card.id} onClick={() => setCardStatus(card, 'approved')}>Restore</button>}
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

createRoot(document.getElementById('root')).render(<App />);
