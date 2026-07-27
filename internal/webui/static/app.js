// SPDX-License-Identifier: MIT

function humanTime(iso) {
  if (!iso) return 'never';
  const t = new Date(iso).getTime();
  if (isNaN(t) || t <= 0) return 'never';
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 172800) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

function humanBytes(n) {
  if (n >= 2 ** 40) return (n / 2 ** 40).toFixed(1) + 'TB';
  if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(1) + 'GB';
  if (n >= 2 ** 20) return (n / 2 ** 20).toFixed(1) + 'MB';
  if (n >= 2 ** 10) return (n / 2 ** 10).toFixed(1) + 'KB';
  return n + 'B';
}

function stateClass(state) {
  if (state === 'in sync') return 'ok';
  if (state === 'syncing' || state === 'scanning') return 'busy';
  // A machine that has never paired is waiting on a person, not broken; red
  // would send someone hunting a fault that doesn't exist.
  if (state === 'awaiting-pair') return 'busy';
  return 'bad'; // offline, stale, full, wrong-drive, error
}

// The one state whose internal name isn't what you'd say out loud.
function stateLabel(state) {
  return state === 'awaiting-pair' ? 'waiting to pair' : state;
}

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined) n.textContent = text;
  return n;
}


// Type icons. Inline SVG in currentColor rather than image files: they inherit
// the text colour they sit beside, cost no extra request, and cannot go missing
// from a build. Four shapes, one per kind of thing this dashboard talks about.
const ICON_PATHS = {
  folder: 'M2 4.5A1.5 1.5 0 0 1 3.5 3h3.1a1 1 0 0 1 .8.4l.9 1.2h4.2A1.5 1.5 0 0 1 14 6.1v5.4A1.5 1.5 0 0 1 12.5 13h-9A1.5 1.5 0 0 1 2 11.5z',
  drive: 'M2.5 3.5h11v6h-11zM4 11.5h8M4.5 6.2h4M11.5 6.2h.6',
  share: 'M8 2.5v3M8 10.5v3M3 8h10M4.6 5.2 8 8l3.4-2.8M4.6 10.8 8 8l3.4 2.8',
  device: 'M2.5 3.5h11v7h-11zM5.5 13h5',
};

function icon(kind) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('class', 'type-icon');
  svg.setAttribute('aria-hidden', 'true');
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  path.setAttribute('d', ICON_PATHS[kind] || ICON_PATHS.folder);
  path.setAttribute('fill', 'none');
  path.setAttribute('stroke', 'currentColor');
  path.setAttribute('stroke-width', '1.3');
  path.setAttribute('stroke-linejoin', 'round');
  path.setAttribute('stroke-linecap', 'round');
  svg.appendChild(path);
  return svg;
}

// --- live status -------------------------------------------------------

// Rows are created once and then updated in place, keyed by folder+target.
// Rebuilding the table on every poll (as this used to) destroyed the DOM each
// time, which killed progress-bar transitions, dropped the table's scroll
// position, and made the whole page flicker.
const rowNodes = new Map();
// Timestamp cells, so the "12s ago" text can tick every second locally instead
// of only moving when a poll happens to land.
const timeCells = new Map();

let lastModel = null;
let polling = 3000;
// True when served by the read-only network view. Controls that would 403 are
// hidden rather than left to fail when tapped.
let readOnly = false;

function rowKey(r) { return r.folder_id + '\u0000' + r.target_name; }

function buildRow(key) {
  const tr = el('tr');
  tr.dataset.key = key;
  const folder = el('td');
  const target = el('td');
  const state = el('td');

  const progress = el('td', 'progress-cell');
  // Bar and label on one line, in fixed columns: the percentages read down the
  // column instead of hanging under each bar at whatever width it happened to
  // be, which is what made these rows two lines tall and ragged.
  const wrap = el('div', 'progress-wrap');
  const bar = el('div', 'bar');
  const fill = el('div', 'bar-fill');
  bar.appendChild(fill);
  const label = el('span', 'bar-label');
  wrap.append(bar, label);
  progress.appendChild(wrap);

  const seen = el('td');
  tr.append(folder, target, state, progress, seen);
  tr._cells = { folder, target, state, fill, label, seen };
  return tr;
}

function updateRow(tr, r) {
  const c = tr._cells;
  setText(c.folder, r.folder_label || r.folder_id);
  setText(c.target, r.target_name);
  setText(c.state, stateLabel(r.state) + (r.stale ? ' ⚠' : ''));
  c.state.className = stateClass(r.state) + ' dot';

  const active = r.state === 'syncing' || r.state === 'scanning';
  const pct = Math.max(0, Math.min(100, r.completion || 0));
  c.fill.style.width = pct + '%';
  c.fill.className = 'bar-fill ' + (active ? 'busy' : stateClass(r.state));
  // A target that is scanning has no denominator yet; stripes say "working"
  // without inventing a number.
  c.fill.classList.toggle('indeterminate', r.state === 'scanning' && !r.total_bytes);

  if (r.total_bytes > 0) {
    setText(c.label, `${humanBytes(r.transferred_bytes || 0)} of ${humanBytes(r.total_bytes)}`);
  } else if (r.need_items > 0) {
    setText(c.label, `${r.need_items} left`);
  } else if (r.state === 'in sync') {
    setText(c.label, '100%');
  } else {
    setText(c.label, '—');
  }

  timeCells.set(tr, r.last_seen);
  setText(c.seen, humanTime(r.last_seen));
  if (r.detail) tr.title = r.detail; else tr.removeAttribute('title');
}

// Only touch the DOM when the value actually changed, so the browser isn't
// re-laying-out unchanged text every second.
function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

function renderRows(st) {
  const table = document.getElementById('rows');
  const body = table.querySelector('tbody');
  const rows = st.rows || [];
  if (rows.length === 0) {
    table.hidden = true;
    rowNodes.clear();
    timeCells.clear();
    body.replaceChildren();
    return;
  }

  const seen = new Set();
  rows.forEach((r, i) => {
    const key = rowKey(r);
    seen.add(key);
    let tr = rowNodes.get(key);
    if (!tr) {
      tr = buildRow(key);
      rowNodes.set(key, tr);
    }
    updateRow(tr, r);
    // Keep DOM order matching the model without recreating anything.
    if (body.children[i] !== tr) body.insertBefore(tr, body.children[i] || null);
  });
  for (const [key, tr] of rowNodes) {
    if (!seen.has(key)) {
      tr.remove();
      rowNodes.delete(key);
      timeCells.delete(tr);
    }
  }
  table.hidden = false;
}

// The folder panel is rebuilt from scratch on every poll, which would wipe an
// open exclude editor out from under someone mid-sentence. While one is open
// the whole panel is left alone; polling resumes on save or cancel.
let editingIgnoresFor = null;

function renderFolders(st) {
  if (editingIgnoresFor !== null) return;
  const sec = document.getElementById('folders-section');
  const list = document.getElementById('folder-list');
  const folders = st.folders || [];
  sec.hidden = folders.length === 0;
  list.replaceChildren();
  for (const f of folders) {
    const li = el('li', 'card');
    // Four slots, always in this order, so every card lines up with the one
    // above it whatever its own content is: icon, name, metadata, actions.
    li.appendChild(icon('folder'));
    li.appendChild(el('strong', null, f.label));
    const meta = el('div', 'card-meta');
    if (f.path) meta.appendChild(el('span', 'muted mono', f.path));
    if (f.ignores && f.ignores.length) {
      meta.appendChild(el('span', 'muted', 'excluding: ' + f.ignores.join(', ')));
    }
    li.appendChild(meta);
    const actions = el('div', 'card-actions');
    if (!readOnly) {
      const edit = el('button', 'small-btn',
        f.ignores && f.ignores.length ? 'edit excludes' : 'exclude files…');
      edit.onclick = () => openIgnoreEditor(li, f);
      actions.appendChild(edit);
      const btn = el('button', 'danger', 'Stop protecting');
      btn.onclick = () => removeFolder(f);
      actions.appendChild(btn);
    }
    li.appendChild(actions);
    list.appendChild(li);
  }
}

// Inline editor for one folder's excludes. Comma-separated, matching the
// wizard, so the same list can be typed the same way in either place.
function openIgnoreEditor(li, f) {
  // The panel is frozen while an editor is open, so the other cards' buttons
  // are still live: only ever show one box, or it isn't clear which folder
  // Save applies to.
  for (const open of li.parentElement.querySelectorAll('.ignore-editor')) open.remove();
  editingIgnoresFor = f.id;
  const box = el('div', 'ignore-editor card-wide');

  const input = el('input');
  input.type = 'text';
  input.value = (f.ignores || []).join(', ');
  input.placeholder = 'scratch, *.iso, assets/raw';
  const save = el('button', 'small-btn', 'Save');
  const cancel = el('button', 'small-btn', 'Cancel');
  box.append(input, save, cancel);
  box.appendChild(el('span', 'muted hint',
    'Separate patterns with commas: a name matches anywhere ("scratch", "*.iso"), ' +
    'a path matches that subfolder ("assets/raw"). Empty excludes nothing. ' +
    'Changes take effect within a few seconds — files that start matching stop ' +
    'being copied, and the copies already on your destinations are moved into ' +
    'version history, not deleted.'));

  // Reopening on the next poll would be surprising, so closing always hands the
  // panel back to the normal render.
  const close = () => { editingIgnoresFor = null; refresh(); };
  cancel.onclick = close;
  save.onclick = async () => {
    save.disabled = true;
    const ignores = input.value.split(',').map(p => p.trim()).filter(p => p !== '');
    const resp = await mutate('/api/folders/' + encodeURIComponent(f.id) + '/ignores', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      // Sent back as read: this editor changes the patterns, not whether the
      // standard exclusions apply.
      body: JSON.stringify({ ignores, no_default_ignores: !!f.no_default_ignores }),
    });
    if (!resp.ok) {
      alert(await resp.text());
      save.disabled = false;
      return;
    }
    close();
  };

  li.appendChild(box);
  input.focus();
}

// A reading older than this is shown greyed with "as of Xm ago" rather than as
// live: the daemon refreshes every minute, so a few minutes of silence means
// the destination has gone offline and the figure is history, not status.
const SPACE_STALE_MS = 5 * 60 * 1000;

// The space readout gets its own full-width line inside the card (see the
// .storage rule in the stylesheet), so it reads cleanly under the metadata
// row rather than crowding it.
function renderSpace(li, t) {
  // A destination that cannot report its free space is not the same as one with
  // no space to report. Hiding the row would make it look exactly like a paired
  // machine's card — healthy — while the reserve goes unenforced there.
  if (t.space_unknown) {
    const row = el('div', 'storage card-wide');
    row.appendChild(el('span', 'bad', 'free space unavailable — reserve not enforced'));
    li.appendChild(row);
    return;
  }
  if (!t.total_bytes) return;
  const free = t.free_bytes || 0;
  const used = Math.max(0, t.total_bytes - free);
  const usedPct = Math.min(100, (used / t.total_bytes) * 100);

  const reported = t.space_reported_at ? new Date(t.space_reported_at).getTime() : 0;
  const stale = reported > 0 && (Date.now() - reported) > SPACE_STALE_MS;

  // Red once the reserve is breached or the drive is nearly full; amber as it
  // gets tight; otherwise a calm "ok" fill. A stale reading is greyed instead.
  let cls;
  if (stale) cls = '';
  else if ((t.min_free_bytes && free <= t.min_free_bytes) || usedPct >= 95) cls = 'bad';
  else if (usedPct >= 85) cls = 'busy';
  else cls = 'ok';

  const row = el('div', 'storage card-wide');
  const bar = el('div', 'bar');
  const fill = el('div', 'bar-fill ' + cls);
  fill.style.width = usedPct + '%';
  if (stale) fill.style.opacity = '0.4';
  bar.appendChild(fill);
  row.appendChild(bar);

  let text = `${humanBytes(free)} free of ${humanBytes(t.total_bytes)}`;
  if (stale) text += ` (as of ${humanTime(t.space_reported_at)})`;
  row.appendChild(el('span', 'muted', text));

  if (t.min_free_bytes) {
    row.appendChild(el('span', 'muted', `· keeping ${humanBytes(t.min_free_bytes)} free`));
  }
  li.appendChild(row);
}

function renderTargets(st) {
  const sec = document.getElementById('targets-section');
  const list = document.getElementById('target-list');
  const targets = st.targets || [];
  sec.hidden = targets.length === 0;
  list.replaceChildren();
  const kind = { drive: 'drive on this computer', share: 'network drive', device: 'paired computer' };
  for (const t of targets) {
    const li = el('li', 'card');
    li.appendChild(icon(t.type === 'share' ? 'share' : t.type === 'device' ? 'device' : 'drive'));
    li.appendChild(el('strong', null, t.name));
    const meta = el('div', 'card-meta');
    // The location is the whole point of this panel: a name like "sdcard"
    // means nothing without it. The network view omits it deliberately, so
    // leave it out rather than showing an empty placeholder.
    if (t.location) meta.appendChild(el('span', 'muted mono', t.location));
    meta.appendChild(el('span', 'muted', kind[t.type] || t.type));
    meta.appendChild(el('span', stateClass(t.state) + ' dot', stateLabel(t.state)));
    meta.appendChild(el('span', 'muted',
      t.all_folders ? 'every folder' : `${t.folder_count} folder(s)`));
    if (t.wake_enabled) meta.appendChild(el('span', 'muted', 'wakes on demand'));
    // Deleting a user's backup history is never silent.
    if (t.reclaim_note) meta.appendChild(el('span', 'busy', t.reclaim_note));
    li.appendChild(meta);
    const actions = el('div', 'card-actions');
    if (!readOnly) {
      const btn = el('button', 'danger', 'Remove');
      btn.onclick = () => removeTarget(t);
      actions.appendChild(btn);
    }
    li.appendChild(actions);
    // Appended last so it drops onto its own line, leaving the metadata row and
    // the Remove button together on the first line.
    renderSpace(li, t);
    list.appendChild(li);
  }
}

async function removeFolder(f) {
  if (!confirm(`Stop backing up ${f.path}?\n\nBackups already written to your destinations are left exactly where they are — this only stops future copying.`)) return;
  const resp = await mutate('/api/folders/' + encodeURIComponent(f.id), { method: 'DELETE' });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

async function removeTarget(t) {
  if (!confirm(`Stop backing up to ${t.name}?\n\nThe backups already on it are left exactly where they are — this only stops future copying.`)) return;
  const resp = await mutate('/api/targets/' + encodeURIComponent(t.name), { method: 'DELETE' });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

function renderArchives(st) {
  const sec = document.getElementById('archives-section');
  if (!st.archives || st.archives.length === 0) { sec.hidden = true; return; }
  sec.hidden = false;
  const body = document.querySelector('#archive-rows tbody');
  body.replaceChildren();
  for (const a of st.archives) {
    const tr = el('tr');
    const cells = [a.name, a.target, a.every, a.state, humanTime(a.last_run)];
    cells.forEach((c, i) => {
      const td = el('td', undefined, c);
      if (i === 3) {
        td.className = a.state === 'ok' ? 'ok' : (a.state === 'never run' || a.needs_password ? 'busy' : 'bad');
        // A snapshot with no stored password never runs; offer to set it here
        // rather than sending the user to the command line.
        if (a.needs_password && !readOnly) {
          const btn = el('button', 'small-btn', 'enter password…');
          btn.onclick = () => enterArchivePassword(a);
          td.appendChild(btn);
        }
      }
      tr.appendChild(td);
    });
    if (a.detail) tr.title = a.detail;
    body.appendChild(tr);
  }
}

async function enterArchivePassword(a) {
  const pw = prompt(`Password for snapshot "${a.name}":`);
  if (!pw) return;
  const resp = await mutate('/api/archives/' + encodeURIComponent(a.name) + '/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: pw }),
  });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

// The lifetime odometer: how much backup-maker has copied since it was set up.
//
// It counts what THIS program writes — drive and share destinations, and
// snapshot zips. Data bound for a paired computer is transferred by the sync
// engine and never passes the counter, so the line says so whenever such a
// destination exists, and a machine that backs up ONLY to paired computers is
// told the figure doesn't cover it rather than being shown a proud zero.
function renderTotals(st) {
  const sec = document.getElementById('totals');
  const t = st.totals;
  // The network view is not given this figure at all; with nothing to render,
  // say nothing.
  if (!t) { sec.hidden = true; return; }

  const counted = t.mirror_targets > 0 || t.bytes > 0;
  if (!counted) {
    if (!t.device_targets) { sec.hidden = true; return; }
    sec.hidden = false;
    document.getElementById('totals-line').textContent =
      'Backed up in total: not counted here — every destination is another computer, ' +
      'and the sync engine transfers those itself.';
    return;
  }

  const partial = t.device_targets > 0
    ? ' · drives and shares only; copies sent to a paired computer are transferred by the sync engine and are not counted'
    : '';
  let text;
  if (!t.files) {
    text = 'Backed up in total: nothing copied yet';
  } else {
    text = `Backed up in total: ${humanBytes(t.bytes)} across ${humanFiles(t.files)}`;
    const since = t.since ? new Date(t.since) : null;
    if (since && !isNaN(since.getTime())) {
      text += ' since ' + since.toLocaleDateString(undefined,
        { day: 'numeric', month: 'long', year: 'numeric' });
    }
  }
  sec.hidden = false;
  document.getElementById('totals-line').textContent = text + partial;
}

function humanFiles(n) {
  return n.toLocaleString() + (n === 1 ? ' file' : ' files');
}

// Machines asking to back up here. Approving one lets it write into this
// computer's receive folder, so the ID is always shown in full: the only real
// check is that it matches what the other machine printed.
function renderPending(sources) {
  const box = document.getElementById('pending');
  box.replaceChildren();
  for (const p of sources) {
    const row = el('div', 'pending-source');
    row.appendChild(el('strong', null, `Machine "${p.name || 'unnamed'}" wants to back up here`));
    row.appendChild(el('span', 'mono', p.device_id));
    if (readOnly) {
      // The network view can't change anything; offering a button that 403s
      // would just look broken.
      row.appendChild(el('span', 'muted', 'approve it on the computer itself'));
    } else {
      const btn = el('button', null, 'Approve');
      btn.onclick = () => acceptPair(p, btn);
      row.appendChild(btn);
      row.appendChild(el('span', 'muted',
        'Check this ID against what "backup-maker pair" shows on that machine before approving.'));
    }
    box.appendChild(row);
  }
}

async function acceptPair(p, btn) {
  if (!confirm(`Approve "${p.name || 'unnamed'}"?\n\n${p.device_id}\n\nIt will be able to store backups on this computer. Only approve it if that ID matches what "backup-maker pair" prints on the other machine.`)) return;
  btn.disabled = true;
  const resp = await mutate('/api/pair/accept', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ device_id: p.device_id }),
  });
  if (!resp.ok) {
    alert(await resp.text());
    btn.disabled = false;
    return;
  }
  refresh();
}

// Backups this computer HOLDS for other machines that have since been changed
// here. Nothing syncs such a change anywhere — the folder is receive-only — so
// it would otherwise stay invisible while this copy quietly stops matching the
// machine it is supposed to protect.
function renderReceived(st) {
  const list = document.getElementById('received-list');
  list.replaceChildren();
  for (const f of (st.received_folders || [])) {
    // A folder with nothing changed is doing its job; only drift is actionable.
    if (!f.changed_items) continue;
    const li = el('li', 'card');
    li.appendChild(el('strong', null, f.label || f.id));
    if (f.source) li.appendChild(el('span', 'muted', `backup from ${f.source}`));
    const size = f.changed_bytes ? ` (${humanBytes(f.changed_bytes)})` : '';
    li.appendChild(el('span', 'bad',
      `${f.changed_items} item(s) changed on this computer${size}`));
    if (readOnly) {
      li.appendChild(el('span', 'muted', 'undo it on the computer itself'));
    } else {
      const btn = el('button', 'danger', 'Revert');
      btn.onclick = () => revertReceived(f, btn);
      li.appendChild(btn);
    }
    list.appendChild(li);
  }
}

// Revert is destructive and asymmetric — it deletes anything that exists only
// here — so the confirmation says that in those words rather than offering to
// "discard changes".
async function revertReceived(f, btn) {
  const name = f.label || f.id;
  const from = f.source || 'the machine that sent it';
  if (!confirm(
    `Restore "${name}" to exactly what ${from} has?\n\n` +
    `${f.changed_items} item(s) here no longer match that machine's copy.\n\n` +
    `Every file ADDED to this folder on this computer will be DELETED.\n` +
    `Every edit made here will be undone, and anything deleted here comes back.\n\n` +
    `Nothing on ${from} changes. This cannot be undone.`)) return;
  btn.disabled = true;
  const resp = await mutate('/api/receive/revert', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ folder_id: f.id }),
  });
  if (!resp.ok) {
    alert(await resp.text());
    btn.disabled = false;
    return;
  }
  refresh();
}

// Show a message when no status could be fetched at all.
//
// #status-line lives inside <main id="dashboard" hidden>, and the dashboard is
// only revealed once a status arrives. Writing the text and returning — which
// is what both failure paths used to do — put the explanation inside a hidden
// container, so the page was simply BLANK: no error, no hint, nothing. That is
// the worst thing this page can do, because the two ways to get here are "not
// signed in" and "the backup daemon is not running".
function showOnlyMessage(text) {
  document.getElementById('status-line').textContent = text;
  if (!Wizard.isOpen() && !AdoptWizard.isOpen()) {
    document.getElementById('dashboard').hidden = false;
  }
}

async function refresh() {
  let st;
  try {
    const resp = await fetch('/api/status');
    if (resp.status === 401) {
      showOnlyMessage('Not signed in. Run "backup-maker web" in a terminal to open the dashboard logged in.');
      return;
    }
    st = await resp.json();
  } catch {
    showOnlyMessage('Cannot reach the backup daemon — your backups may not be running. Start it with "backup-maker daemon".');
    return;
  }
  applyStatus(st);
}

// applyStatus paints one snapshot. Shared by the poll and the event stream so
// there is exactly one render path, whichever way the snapshot arrived.
// Which build this is, so "am I running the version with that fix?" can be
// answered without opening a terminal. Absent on the network view, which
// strips it — an exact version says which known bugs apply, and that view has
// no password.
function showVersion(st) {
  const el = document.getElementById('version');
  if (!el) return;
  if (!st.version) { el.hidden = true; return; }
  // "v" belongs on a release number, not on "dev" — "vdev-dirty" reads like a
  // typo. A build that says dev, or dirty, is telling you it is not a release,
  // which is worth seeing plainly rather than dressed up as one.
  // Labelled, because the number alone does not say what it is — and on a
  // build that is not a release the string is "dev-dirty", which reads as
  // nothing at all without the word in front of it.
  //
  // "v" belongs on a release number, not on "dev": "vdev-dirty" reads like a
  // typo. A build that says dev, or dirty, is telling you it is not a release,
  // which is worth seeing plainly rather than dressed up as one.
  const num = /^\d/.test(st.version) ? 'v' + st.version : st.version;
  // The commit disambiguates two local builds, which "dev-dirty" cannot.
  // Pointless noise on a release, where the tag already says exactly which
  // build this is.
  const showCommit = st.commit && !/^\d/.test(st.version);
  el.textContent = 'version ' + num + (showCommit ? ' · ' + st.commit : '');
  el.title = 'The backup-maker build this dashboard is served by';
  el.hidden = false;
}

function applyStatus(st) {
  showVersion(st);
  const line = document.getElementById('status-line');
  lastModel = st;

  document.getElementById('machine').textContent = st.machine_name;
  const engine = document.getElementById('engine');
  if (st.engine_ok) {
    engine.textContent = 'engine running';
    engine.className = 'badge ok';
  } else if (st.engine_needed) {
    engine.textContent = 'engine down';
    engine.className = 'badge bad';
  } else {
    engine.textContent = 'local-only mode';
    engine.className = 'badge';
  }
  // The network view redacts identifiers, so don't render a placeholder that
  // implies something is wrong or starting up.
  const identity = document.getElementById('identity');
  identity.hidden = !!st.redacted;
  if (!st.redacted) {
    document.getElementById('device-id').textContent =
      st.device_id || (st.engine_needed ? '(engine starting…)' : '(appears when machine sync is set up)');
    // The QR is fetched only once there is an ID to encode — asking earlier
    // would 503 on every load of a machine with no sync configured. Once
    // loaded it is left alone: the ID doesn't change while the page is open.
    const qr = document.getElementById('device-qr');
    const qrImg = document.getElementById('device-qr-img');
    qr.hidden = !st.device_id;
    if (st.device_id && !qrImg.src) qrImg.src = '/api/pair/qr';
  }

  // A fresh install goes straight into the wizard rather than showing an
  // empty table that explains nothing. Never on the read-only view: setting
  // up a backup is only possible on the machine itself.
  if (!readOnly && !st.setup_complete && !Wizard.isOpen() && !AdoptWizard.isOpen()) {
    Wizard.open(st, { firstRun: true });
  }
  // Both panels start hidden in the markup, so an already-configured install
  // must be revealed here — otherwise the dashboard only ever appears after
  // finishing the wizard, and a normal page load shows a blank screen. The
  // adopt flow takes over the page too, so it counts as "a wizard is open".
  document.getElementById('dashboard').hidden = Wizard.isOpen() || AdoptWizard.isOpen();

  // A STANDING PROPERTY, NOT A STATUS. This used to alternate with "Nothing
  // backed up yet", which the verdict above now says properly — and which meant
  // the one claim people choose this program for vanished from the screen
  // exactly when a new user was reading it hardest. It is also load-bearing:
  // backups to drives, NAS boxes and shared folders never touch the internet,
  // and the single exception (a pinned, checksum-verified sync engine, fetched
  // once, only if you pair a second machine) is the user's own decision.
  //
  // If anything here ever reaches the internet by default, this line and the
  // matching promise in README.md both stop being true and must change with it.
  setText(line, 'Everything stays on your own network.');
  renderVerdict(st);
  renderRows(st);
  renderFolders(st);
  renderTargets(st);
  renderArchives(st);
  renderTotals(st);
  renderSettings(st);

  const recSec = document.getElementById('receive-section');
  if (st.receive && st.receive.enabled) {
    recSec.hidden = false;
    // The redacted view has no root path — say the useful half rather than
    // interpolating an undefined into the sentence.
    document.getElementById('receive-line').textContent = st.receive.root
      ? `Also receiving backups from other machines into ${st.receive.root}`
      : 'Also receiving backups from other machines.';
    renderReceived(st);
  } else {
    recSec.hidden = true;
  }

  const pendSec = document.getElementById('pending-section');
  if (st.pending_sources && st.pending_sources.length > 0) {
    pendSec.hidden = false;
    renderPending(st.pending_sources);
  } else if (readOnly && st.pending_source_count > 0) {
    // The network view is told THAT something wants to back up here, never
    // which machine or from where — approving is a decision for the dashboard.
    const n = st.pending_source_count;
    pendSec.hidden = false;
    document.getElementById('pending').replaceChildren(
      el('p', 'muted', n === 1
        ? 'A computer is asking to back up here. Approve it on this machine.'
        : `${n} computers are asking to back up here. Approve them on this machine.`));
  } else {
    pendSec.hidden = true;
  }

  // Poll hard while something is moving, then back off so an idle dashboard
  // isn't a busy-loop.
  const active = (st.rows || []).some(r => r.state === 'syncing' || r.state === 'scanning');
  polling = active ? 1000 : 5000;
}

// Relative times advance locally every second, so "8s ago" keeps counting
// between polls instead of freezing until the next fetch.
setInterval(() => {
  for (const [tr, iso] of timeCells) {
    setText(tr._cells.seen, humanTime(iso));
  }
}, 1000);

// --- getting snapshots -------------------------------------------------
//
// Preferred path is the event stream: the daemon writes only when something
// actually changed, so an idle dashboard is silent. Polling stays as the
// fallback for browsers without EventSource and for a stream that drops.

let pollTimer = null;

function startPolling() {
  if (pollTimer !== null) return;
  pollTimer = setTimeout(function loop() {
    refresh().finally(() => {
      // Null means the stream came back while that fetch was in flight; don't
      // reschedule, or the page would poll and stream at the same time.
      if (pollTimer === null) return;
      pollTimer = setTimeout(loop, polling);
    });
  }, 0);
}

function stopPolling() {
  if (pollTimer === null) return;
  clearTimeout(pollTimer);
  pollTimer = null;
}

if (window.EventSource) {
  // Authenticated by the session cookie — EventSource cannot send headers.
  const stream = new EventSource('/api/events');
  stream.onmessage = (e) => {
    stopPolling();
    try {
      applyStatus(JSON.parse(e.data));
    } catch { /* a malformed frame is not worth tearing the page down for */ }
  };
  // The browser reconnects by itself where it can; polling covers the gap, and
  // the next message stops it again. The read-only network view lands here too
  // — /api/events isn't on its allow-list, so it simply keeps polling.
  stream.onerror = startPolling;
} else {
  startPolling();
}

document.getElementById('open-wizard').addEventListener('click', () => {
  Wizard.open(lastModel, { firstRun: false });
});

// Ask once, up front, whether this page is the read-only network view.
(async function detectReadOnly() {
  try {
    const resp = await fetch('/api/ping');
    const d = await resp.json();
    readOnly = !!d.read_only;
  } catch { /* the status poll reports connection problems */ }
  if (!readOnly) return;
  document.getElementById('open-wizard').hidden = true;
  const banner = el('div', 'notice subtle',
    'Read-only view. You can watch backups from here, but setting them up or ' +
    'changing them is only possible on the computer running backup-maker.');
  document.getElementById('dashboard').prepend(banner);
  offerHomeScreen();
})();

// The read-only view is the only page a phone can open — the dashboard binds
// loopback and refuses everything else — so this is where a home-screen icon
// is worth suggesting.
//
// It is wording, not a button: no browser exposes a way for a page to add
// itself to the home screen (the install prompt needs a full PWA, and iOS
// Safari ignores it), and a control that looked like it worked but didn't
// would be worse than saying nothing.
function offerHomeScreen() {
  const hint = document.getElementById('homescreen-hint');
  if (!hint || !window.matchMedia('(max-width: 40rem)').matches) return;
  const ios = /iphone|ipad|ipod/i.test(navigator.userAgent);
  hint.textContent = ios
    ? ' Keep it a tap away: Share, then "Add to Home Screen".'
    : ' Keep it a tap away: your browser\'s menu, then "Add to Home screen".';
  hint.hidden = false;
}

// Expose for the wizard's "finished" callback.
window.refreshDashboard = refresh;


// The verdict: this page's single job, answered in one line.
//
// Derived from the destinations rather than the rows, because a destination is
// the thing that can actually fail — a folder is only ever as backed up as the
// places it reaches. Counted, never listed: the panels below are the list, and
// a headline that tries to be both stops being readable at a glance.
function renderVerdict(st) {
  const box = document.getElementById('verdict');
  const dot = document.getElementById('verdict-dot');
  const line = document.getElementById('verdict-line');
  if (!box || !line) return;
  if (dot) dot.hidden = false;

  const targets = st.targets || [];
  const rows = st.rows || [];
  const broken = targets.filter((t) => t.state === 'stale' || t.state === 'full' ||
    t.state === 'unrecognised' || t.state === 'error');
  const working = rows.some((r) => r.state === 'syncing' || r.state === 'scanning');
  const offline = targets.filter((t) => t.state === 'offline');

  let state = 'ok';
  let text;
  if (targets.length === 0) {
    state = '';
    text = 'Nothing is being backed up yet';
  } else if (broken.length > 0) {
    state = 'bad';
    text = broken.length === 1
      ? `${broken[0].name} needs attention`
      : `${broken.length} destinations need attention`;
  } else if (working) {
    state = 'busy';
    text = 'Backing up now';
  } else if (offline.length === targets.length) {
    state = 'busy';
    text = offline.length === 1
      ? `${offline[0].name} is not connected`
      : 'No destination is connected';
  } else {
    text = offline.length > 0
      ? `Backups are current — ${offline.length} destination${offline.length > 1 ? 's' : ''} not connected`
      : 'Everything is backed up';
  }
  setText(line, text);
  box.className = 'verdict' + (state ? ' ' + state : '');
}

// --- settings ----------------------------------------------------------
//
// Every box saves as soon as it is clicked. There is no Save button because
// there is nothing to lose: each of these is one boolean, applied immediately,
// and a form that has to be submitted invites the state where the screen and
// the daemon disagree.
//
// The panel is rendered from the daemon's answer, never from what was clicked,
// so a rejected change snaps back rather than lying about what is in force.
const SETTING_FIELDS = [
  ['set-desktop-alerts', 'desktop_alerts'],
  ['set-alert-backups-stopped', 'alert_backups_stopped'],
  ['set-alert-snapshot-failed', 'alert_snapshot_failed'],
  ['set-alert-unrecognised-storage', 'alert_unrecognised_storage'],
  ['set-alert-pair-requests', 'alert_pair_requests'],
  ['set-lan-view', 'lan_view'],
  ['set-webhook-enabled', 'webhook_enabled'],
  ['set-webhook-minimal', 'webhook_minimal'],
  ['set-ntfy-enabled', 'ntfy_enabled'],
  ['set-ntfy-minimal', 'ntfy_minimal'],
];

// settingsBusy suppresses the re-render that a save's own status update would
// otherwise trigger under the user's cursor.
let settingsBusy = false;

function renderSettings(st) {
  const sec = document.getElementById('settings-section');
  if (!sec) return;
  // Not on the read-only view: it may not change anything, and it is not told
  // what is switched off here.
  if (readOnly || !st.settings || !st.setup_complete) { sec.hidden = true; return; }
  sec.hidden = false;
  if (settingsBusy) return;

  for (const [id, key] of SETTING_FIELDS) {
    const box = document.getElementById(id);
    if (box) box.checked = !!st.settings[key];
  }
  // The categories are subordinate to the master switch: with alerts off
  // entirely, nothing below it is in force, and the panel should not imply
  // otherwise.
  const kinds = document.getElementById('alert-kinds');
  const on = !!st.settings.desktop_alerts;
  kinds.disabled = !on;
  if (kinds.disabled) kinds.setAttribute('disabled', ''); else kinds.removeAttribute('disabled');
  for (const [id] of SETTING_FIELDS.slice(1, 5)) {
    const box = document.getElementById(id);
    if (box) box.disabled = !on;
  }

  // Where the network view is actually reachable, so it can be typed into a
  // phone. Only shown when the listener really came up: the setting being on
  // and the socket being bound are different facts, and a machine with no LAN
  // address has the first without the second.
  renderDeliveryState(st);

  // The webhook's own controls appear only once it is switched on.
  const hookBox = document.getElementById('webhook-config');
  if (hookBox) hookBox.hidden = !st.settings.webhook_enabled;
  const urlState = document.getElementById('webhook-url-state');
  if (urlState) {
    // The address itself is never sent to the browser — it is a credential.
    // Saying whether one is stored is all the panel needs, and all it gets.
    urlState.textContent = st.settings.webhook_url_set
      ? 'An address is saved. Type a new one to replace it, or save an empty box to remove it.'
      : 'No address saved yet — alerts have nowhere to go until you save one.';
    urlState.className = st.settings.webhook_url_set ? 'muted small' : 'small bad';
  }

  // ntfy, the same way. Its topic is treated as a credential like the webhook
  // address — on the public server the topic name is the only thing between a
  // stranger and every alert this machine sends — so the panel is told that one
  // exists and never what it is.
  const ntfyBox = document.getElementById('ntfy-config');
  if (ntfyBox) ntfyBox.hidden = !st.settings.ntfy_enabled;
  const topicState = document.getElementById('ntfy-topic-state');
  if (topicState) {
    topicState.textContent = st.settings.ntfy_topic_set
      ? 'A topic is saved. Type a new one to replace it, or save an empty box to remove it.'
      : 'No topic saved yet — alerts have nowhere to go until you save one.';
    topicState.className = st.settings.ntfy_topic_set ? 'muted small' : 'small bad';
  }
  const tokenState = document.getElementById('ntfy-token-state');
  if (tokenState) {
    // Not having one is perfectly normal — a public ntfy.sh topic needs none —
    // so this never renders as a fault, unlike a missing topic.
    tokenState.textContent = st.settings.ntfy_token_set
      ? 'A token is saved. Type a new one to replace it, or save an empty box to remove it.'
      : 'No token saved. A public topic does not need one.';
    tokenState.className = 'muted small';
  }

  // Who may open it, and the devices that have asked. Both are meaningless
  // while the view is off, so neither is shown then.
  const access = document.getElementById('lan-access');
  const mode = st.settings.lan_view_access || 'all';
  access.hidden = !st.settings.lan_view;
  const allBox = document.getElementById('lan-access-all');
  const approvedBox = document.getElementById('lan-access-approved');
  if (allBox) allBox.checked = mode === 'all';
  if (approvedBox) approvedBox.checked = mode === 'approved';
  // The "no password" warning is only true in one of the two modes. Leaving it
  // up in the other would be the sort of stale reassurance this project treats
  // as a defect — in reverse.
  const warn = document.getElementById('lan-open-warning');
  if (warn) warn.hidden = st.settings.lan_view && mode === 'approved';
  renderLANDevices(st, mode);

  const urlLine = document.getElementById('lan-view-url');
  if (st.settings.lan_view && st.settings.lan_view_url) {
    urlLine.textContent = 'Reachable at ' + st.settings.lan_view_url +
      ' — this address can change when your router reassigns it; reserve it to keep the link working.';
    urlLine.className = 'muted small';
    urlLine.hidden = false;
  } else if (st.settings.lan_view) {
    // The daemon's own reason, never a guessed one. The commonest failure is
    // another backup-maker already holding the port, which the old wording
    // reported as "no network address was found" — plainly untrue, and it sent
    // whoever read it looking at the wrong thing entirely.
    urlLine.textContent = 'Switched on, but not listening: ' +
      (st.settings.lan_view_error || 'the reason was not reported.');
    urlLine.className = 'small bad';
    urlLine.hidden = false;
  } else {
    urlLine.hidden = true;
  }
}

function settingsMessage(text, isError) {
  const msg = document.getElementById('settings-msg');
  if (!msg) return;
  msg.textContent = text;
  msg.className = isError ? 'small bad' : 'muted small';
  msg.hidden = !text;
}

async function saveSetting(key, value) {
  settingsBusy = true;
  settingsMessage('Saving…', false);
  try {
    const resp = await mutate('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ [key]: value }),
    });
    if (!resp.ok) {
      settingsMessage(await resp.text() || 'Could not save that setting.', true);
    } else {
      settingsMessage('Saved.', false);
    }
  } catch {
    settingsMessage('Could not reach the backup daemon — the setting was not changed.', true);
  } finally {
    settingsBusy = false;
    // Repaint from the daemon, so a refused change is visibly refused.
    refresh();
  }
}

function wireSettings() {
  for (const [id, key] of SETTING_FIELDS) {
    const box = document.getElementById(id);
    if (box) box.addEventListener('change', () => saveSetting(key, box.checked));
  }
}

// Test one delivery method. The button reports the daemon's actual answer —
// a notifier that failed says so — because the whole point is to find out
// before a real fault does.
async function testDelivery(btn) {
  const method = btn.dataset.method;
  const msg = document.getElementById('delivery-test-msg');
  const say = (text, bad) => {
    msg.textContent = text;
    msg.className = bad ? 'small bad' : 'muted small';
    msg.hidden = false;
  };
  btn.disabled = true;
  say('Sending…', false);
  try {
    const resp = await mutate('/api/alerts/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method }),
    });
    if (resp.ok) {
      // Whether THIS method is switched on, not whether any of them is. The
      // check used to read desktop_alerts whatever button was pressed, which
      // told somebody testing a working webhook that their alerts were off.
      const key = { desktop: 'desktop_alerts', webhook: 'webhook_enabled', ntfy: 'ntfy_enabled' }[method];
      const st = lastModel && lastModel.settings;
      const off = !!(st && key && !st[key]);
      say(off
        ? 'Sent — but this method is switched off above, so real problems will not go out through it.'
        : method === 'desktop'
          ? 'Sent. If nothing appeared, this machine cannot show notifications.'
          : 'Sent. If nothing arrived, check where it was sent rather than this computer.', off);
    } else {
      say((await resp.text()).trim() || 'The test could not be delivered.', true);
    }
  } catch {
    say('Could not reach the backup daemon.', true);
  } finally {
    btn.disabled = false;
  }
}

// Wired here, at the very end, and NOT beside the other listeners further up:
// SETTING_FIELDS is a const, so a call placed above it hits the temporal dead
// zone and throws before any of this runs — which is exactly what happened, and
// what left the panel on screen with every box dead.
for (const btn of document.querySelectorAll('.test-delivery')) {
  btn.addEventListener('click', () => testDelivery(btn));
}
wireSettings();

// The devices that have asked to watch backups from the network.
//
// Waiting ones come first and carry the code the device itself is showing, so
// approving is a comparison rather than a guess. Approved ones stay listed so
// revoking is possible without hunting for where the list lives.
function renderLANDevices(st, mode) {
  const box = document.getElementById('lan-devices');
  const list = document.getElementById('lan-device-list');
  const title = document.getElementById('lan-devices-title');
  if (!box || !list) return;
  const devices = (st.settings && st.settings.lan_devices) || [];
  if (!st.settings.lan_view || mode !== 'approved' || devices.length === 0) {
    box.hidden = true;
    return;
  }
  box.hidden = false;
  const waiting = devices.filter((d) => !d.approved).length;
  title.textContent = waiting > 0
    ? `${waiting} device${waiting > 1 ? 's' : ''} waiting for approval`
    : 'Approved devices';

  list.replaceChildren();
  const ordered = devices.slice().sort((a, b) => (a.approved === b.approved ? 0 : a.approved ? 1 : -1));
  for (const d of ordered) {
    const li = el('li', 'card');
    li.appendChild(icon('device'));
    li.appendChild(el('strong', 'mono', d.code));
    const meta = el('div', 'card-meta');
    meta.appendChild(el('span', 'muted', d.kind || 'a device'));
    if (d.addr) meta.appendChild(el('span', 'muted mono', d.addr));
    meta.appendChild(el('span', d.approved ? 'ok dot' : 'busy dot',
      d.approved ? 'approved' : 'waiting'));
    li.appendChild(meta);
    const actions = el('div', 'card-actions');
    if (!d.approved) {
      const ok = el('button', null, 'Approve');
      ok.onclick = () => lanDeviceAction(d.code, 'approve', ok);
      actions.appendChild(ok);
    }
    const no = el('button', 'danger', d.approved ? 'Revoke' : 'Deny');
    no.onclick = () => lanDeviceAction(d.code, 'forget', no);
    actions.appendChild(no);
    li.appendChild(actions);
    list.appendChild(li);
  }
}

async function lanDeviceAction(code, what, btn) {
  btn.disabled = true;
  const url = what === 'approve'
    ? '/api/lan-devices/' + encodeURIComponent(code) + '/approve'
    : '/api/lan-devices/' + encodeURIComponent(code);
  try {
    const resp = await mutate(url, { method: what === 'approve' ? 'POST' : 'DELETE' });
    if (!resp.ok) settingsMessage((await resp.text()).trim(), true);
  } catch {
    settingsMessage('Could not reach the backup daemon.', true);
  } finally {
    btn.disabled = false;
    refresh();
  }
}

// The access choice saves like every other setting: on click, immediately.
for (const box of document.querySelectorAll('input[name="lan-access"]')) {
  box.addEventListener('change', () => {
    if (box.checked) saveSetting('lan_view_access', box.value);
  });
}

// Saving one of the credentials a delivery method needs — the webhook address,
// the ntfy topic, the ntfy token.
//
// Each has its OWN Save button rather than saving as you type: a half-typed URL
// is not an address, and every keystroke would be a config write. They are also
// saved one at a time rather than as a group, which matters for the ntfy pair —
// none of these is ever sent back to the browser, so a shared Save would post an
// empty token box (the only state it can be in) and silently drop the token
// whenever somebody edited the topic.
function wireCredentialSave(buttonId, inputId, key, savedMsg) {
  const btn = document.getElementById(buttonId);
  const input = document.getElementById(inputId);
  if (!btn || !input) return;
  btn.addEventListener('click', async () => {
    btn.disabled = true;
    settingsMessage('Saving…', false);
    try {
      const resp = await mutate('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ [key]: input.value.trim() }),
      });
      if (!resp.ok) {
        settingsMessage((await resp.text()).trim() || 'Could not save that.', true);
      } else {
        // Cleared on purpose once saved: leaving a credential sitting in a
        // form field is how it ends up in a screenshot or a password manager's
        // autofill for the wrong site.
        input.value = '';
        settingsMessage(savedMsg, false);
      }
    } catch {
      settingsMessage('Could not reach the backup daemon.', true);
    } finally {
      btn.disabled = false;
      refresh();
    }
  });
}

wireCredentialSave('save-webhook-url', 'set-webhook-url', 'webhook_url', 'Address saved.');
wireCredentialSave('save-ntfy-topic', 'set-ntfy-topic', 'ntfy_topic_url', 'Topic saved.');
wireCredentialSave('save-ntfy-token', 'set-ntfy-token', 'ntfy_token', 'Token saved.');

// How each delivery method last performed, shown under that method.
//
// This is the one fault backup-maker cannot report by alerting about it: if the
// route the alert would travel is broken, the alert never arrives to say so. So
// it is stated on the page instead, next to the switch that turned it on.
function renderDeliveryState(st) {
  const rows = (st.settings && st.settings.delivery) || [];
  const by = {};
  for (const r of rows) by[r.method] = r;
  for (const el of document.querySelectorAll('.delivery-state')) {
    const info = by[el.dataset.for];
    if (!info || !info.at) { el.hidden = true; continue; }
    el.hidden = false;
    if (info.ok) {
      el.textContent = 'Last delivered ' + humanTime(info.at) + '.';
      el.className = 'delivery-state small muted';
    } else {
      // The daemon's own message, never a guess at what went wrong.
      el.textContent = 'Last attempt failed ' + humanTime(info.at) + ' — ' + (info.error || 'no reason given') +
        '. Alerts are not reaching this method.';
      el.className = 'delivery-state small bad';
    }
  }
}
