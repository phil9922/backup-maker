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

function stateClass(state, row) {
  if (state === 'in sync') return 'ok';
  // Nothing is wrong here, so nothing should be red: this folder is waiting on
  // a decision, not reporting a fault.
  if (state === 'no destination yet') return 'muted';
  if (state === 'syncing' || state === 'scanning') {
    // Green once there is a copy on the destination: the dot answers the same
    // question the label does, and amber next to "backed up" would take the
    // reassurance straight back. Amber is for the first backup, which really
    // is still in progress.
    return row && !row.first_backup ? 'ok' : 'busy';
  }
  // A machine that has never paired is waiting on a person, not broken; red
  // would send someone hunting a fault that doesn't exist.
  if (state === 'awaiting-pair') return 'busy';
  return 'bad'; // offline, stale, full, wrong-drive, name-clash, error
}

// The states whose internal names aren't what you'd say out loud.
//
// "SCANNING" AND "SYNCING" ANSWER THE WRONG QUESTION. They say what the program
// is doing; the only question this column is ever read for is whether the files
// are safe. A destination holding a complete copy showed "scanning" for minutes
// at a time and left the honest reading as "I can't tell whether I have a
// backup" — which is the worst thing a backup tool can say, and it was saying it
// while everything was fine.
//
// So the column answers the safety question and the progress column says what is
// happening right now. A destination that has completed a pass has a backup on
// it, and keeps saying so while it looks for changes. One that has never
// finished a pass has no copy to promise yet, and says that instead.
function stateLabel(state, row) {
  if (state === 'scanning' || state === 'syncing') {
    return row && row.first_backup ? 'first backup' : 'backed up';
  }
  if (state === 'in sync') return 'backed up';
  if (state === 'awaiting-pair') return 'waiting to pair';
  if (state === 'name-clash') return 'name already in use';
  return state;
}

// What a state means, where the name alone doesn't say. Only name-clash needs
// this: it is the one state a person cannot act on without being told what the
// remedy is, and the remedy is not guessable.
function stateHelp(state) {
  if (state === 'no destination yet') {
    return 'This folder is being watched but has nowhere to send copies yet. ' +
      'Add a destination and it starts backing up within seconds.';
  }
  if (state !== 'name-clash') return '';
  return 'Another computer already backs up to this destination under the same ' +
    'name. Nothing is being written, because the two would delete each ' +
    "other's files. Set this destination up again in a folder of its own on that storage.";
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
// Whether this page load has painted a status snapshot yet. Guards the one
// thing that may only happen on arrival: throwing the first-run wizard over
// the whole page. See where it is read in applyStatus.
let paintedOnce = false;
// True when served by the read-only network view. Controls that would 403 are
// hidden rather than left to fail when tapped.
let readOnly = false;

function rowKey(r) { return r.folder_id + '\u0000' + r.target_name; }

function buildRow(key) {
  const tr = el('tr');
  tr.dataset.key = key;
  // The folder cell carries the name and the path it stands for. A label on
  // its own is not enough to act on: two folders called "Documents" on one
  // machine are not the same promise, and "stop" against the wrong one is a
  // decision made on a guess.
  const folder = el('td', 'folder-cell');
  const folderName = el('strong');
  const folderPath = el('div', 'muted mono small');
  folder.append(folderName, folderPath);
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
  const actions = el('td', 'row-actions');
  // Labels for the stacked layout on a narrow screen, where the header row is
  // gone and each cell has to say what it is.
  folder.dataset.label = 'Folder';
  target.dataset.label = 'Destination';
  state.dataset.label = 'State';
  progress.dataset.label = 'Progress';
  seen.dataset.label = 'Last sync';
  tr.append(folder, target, state, progress, seen, actions);
  tr._cells = { folder, folderName, folderPath, target, state, fill, label, seen, actions };
  return tr;
}

// updateRow fills one folder×destination row. first says whether this is the
// leading row of its folder's group: the name, path and buttons appear once
// per folder, and the continuation rows leave those cells empty so a folder
// with three destinations reads as one block rather than three repetitions.
function updateRow(tr, r, folder, first) {
  const c = tr._cells;
  setText(c.folderName, first ? (r.folder_label || r.folder_id) : '');
  // Stacked on a phone, every card is on its own — the folder name is only in
  // the first row of a group, so without this the second destination's card
  // would not say which folder it is about.
  tr.dataset.folder = r.folder_label || r.folder_id || '';
  setText(c.folderPath, first && folder && folder.path ? folder.path : '');
  tr.classList.toggle('grouped', !first);
  renderRowActions(c.actions, first ? folder : null, tr);
  setText(c.target, r.target_name || '—');
  // The dot goes on an inner span rather than the cell itself. A cell has one
  // ::before, and on a narrow screen that is spent on the "State" label — with
  // both rules aiming at it the marker and the word drew over each other.
  if (!c.stateText) {
    c.stateText = el('span');
    c.state.replaceChildren(c.stateText);
  }
  setText(c.stateText, stateLabel(r.state, r) + (r.stale ? ' ⚠' : ''));
  c.stateText.className = stateClass(r.state, r) + ' dot';
  // A state whose name doesn't tell you what to do about it gets the
  // explanation on hover rather than a row that just reads red.
  c.state.title = stateHelp(r.state);

  const active = r.state === 'syncing' || r.state === 'scanning';
  const pct = Math.max(0, Math.min(100, r.completion || 0));
  c.fill.style.width = pct + '%';
  c.fill.className = 'bar-fill ' + (active ? 'busy' : stateClass(r.state));
  // A scan with a denominator drives a real bar. Stripes are only for the part
  // before the source walk has finished, when there genuinely is no number.
  const scanning = r.state === 'scanning';
  // A PHASE ONLY MEANS ANYTHING WHILE THE ROW IS SCANNING. This asked about
  // r.phase alone, so a phase left set by the last pass went on being narrated
  // — an idle, fully-backed-up folder showed "checking for deleted files:
  // 72,555" beside an animated bar for as long as nothing changed. The engine no
  // longer leaves it set (endScan), and this no longer trusts it if it is: the
  // CLI has always required the scanning state before printing a phase, and the
  // two surfaces disagreeing is what let it go unnoticed.
  const tidying = scanning && r.phase === 'tidying';
  // A phase with no denominator gets stripes rather than a full bar: a solid
  // 100% that sits still for minutes is what made this look stalled.
  c.fill.classList.toggle('indeterminate',
    (scanning && !r.total_bytes && !r.scan_total) || tidying);
  if (scanning && !r.total_bytes && r.scan_total > 0) {
    c.fill.style.width = Math.min(100, ((r.scanned_files || 0) / r.scan_total) * 100) + '%';
  }

  if (tidying) {
    // The tail of a pass: looking for things deleted from the source. It has
    // no denominator and it is where the minutes go on a network destination,
    // so it says what it is doing rather than leaving a full amber bar to be
    // read as a stall.
    setText(c.label, r.scanned_files
      ? `checking for deleted files: ${r.scanned_files.toLocaleString()}`
      : 'checking for deleted files…');
  } else if (r.total_bytes > 0) {
    setText(c.label, `${humanBytes(r.transferred_bytes || 0)} of ${humanBytes(r.total_bytes)}`);
  } else if (r.need_items > 0) {
    setText(c.label, `${r.need_items} left`);
  } else if (scanning) {
    // Deciding what needs copying, which against a network drive is minutes of
    // work before a byte moves. Saying how many files it has looked at is the
    // difference between "working" and "stuck" — this column previously showed
    // a dash beside a full bar, which read as finished. Out of how many is the
    // rest of it: a bare count cannot say whether it is nearly done.
    setText(c.label, scanLabel(r));
  } else if (r.state === 'in sync') {
    setText(c.label, '100%');
  } else {
    setText(c.label, '—');
  }

  // "never" is for a destination nothing has been written to. A first pass
  // takes minutes and has no completed sync to timestamp yet, which is not the
  // same thing — and printing "never" beside a bar that is visibly moving is
  // how a destination holding a complete copy read as untouched. Dropped from
  // timeCells too, or the one-second ticker would put "never" straight back.
  if (r.first_backup) {
    timeCells.delete(tr);
    setText(c.seen, 'first backup running');
  } else {
    timeCells.set(tr, r.last_seen);
    setText(c.seen, humanTime(r.last_seen));
  }
  if (r.detail) tr.title = r.detail; else tr.removeAttribute('title');
}

// What a scanning row says it is doing.
//
// The three stages of a scan count three different things — files in your
// folder, files already on the destination, files compared — and on a network
// share the middle one is where the minutes go. One label for all three said
// "checked 0 of 70,827" for minutes at a stretch, describing a stage that had
// not started, which reads exactly like being stuck.
function scanLabel(r) {
  const done = (r.scanned_files || 0).toLocaleString();
  // One sentence for the whole scan, because from outside it is one activity:
  // looking for what changed. The stage only decides which counter is honest to
  // show against it.
  const what = r.first_backup ? 'working out what to copy' : 'checking for changes';
  if (r.phase === 'source' && !r.scanned_files) return what + '…';
  if (r.scan_total > 0) return `${what}: ${done} of ${r.scan_total.toLocaleString()}`;
  if (r.scanned_files) return `${what}: ${done} so far`;
  return what + '…';
}

// Only touch the DOM when the value actually changed, so the browser isn't
// re-laying-out unchanged text every second.
function setText(node, text) {
  if (node.textContent !== text) node.textContent = text;
}

// The table is rebuilt from the model on every poll, which would wipe an open
// exclude editor out from under someone mid-sentence. While one is open the
// table is left alone; polling resumes on save or cancel.
let editingIgnoresFor = null;
// The archives table rebuilds on every poll too, so an open editor there needs
// the same freeze the exclude editor has: a form that vanishes mid-sentence
// once a second is not a form.
let editingArchive = null;

// renderRows draws the one table: every folder that has a live mirror, its
// destinations grouped beneath it, and its actions on the leading row.
function renderRows(st) {
  // Rebuilding while someone is typing in the exclude editor would wipe the
  // box out from under them mid-sentence. Progress stops updating for the few
  // seconds it is open, which is the cheaper of the two costs.
  if (editingIgnoresFor !== null) return;
  const table = document.getElementById('rows');
  const sec = document.getElementById('folders-section');
  const body = table.querySelector('tbody');

  // The editor is not one of the rows this function tracks, so nothing else
  // would ever take it away: closing it only clears the flag above. It also
  // has to go before the ordering below, which addresses rows by index and
  // would otherwise count it.
  for (const stale of body.querySelectorAll('tr.ignore-row')) stale.remove();

  // Only folders with a live mirror. A snapshot-only folder belongs under
  // Timed snapshots, where its schedule and password are — listing it here
  // would claim a protection it does not have.
  const folders = new Map();
  for (const f of st.folders || []) {
    if (f.continuous !== false) folders.set(f.id, f);
  }

  const byFolder = new Map();
  for (const r of st.rows || []) {
    if (!folders.has(r.folder_id)) continue;
    if (!byFolder.has(r.folder_id)) byFolder.set(r.folder_id, []);
    byFolder.get(r.folder_id).push(r);
  }
  // A folder with nowhere to send its copies would otherwise vanish from this
  // page entirely — and it is the folder most in need of being looked at.
  const ordered = [];
  for (const [id, f] of folders) {
    const rows = byFolder.get(id);
    if (rows && rows.length) {
      ordered.push(...rows);
      continue;
    }
    ordered.push({ folder_id: id, folder_label: f.label, target_name: '', state: 'no destination yet' });
  }

  if (ordered.length === 0) {
    table.hidden = true;
    if (sec) sec.hidden = true;
    rowNodes.clear();
    timeCells.clear();
    body.replaceChildren();
    return;
  }

  const seen = new Set();
  let lastFolder = null;
  ordered.forEach((r, i) => {
    const key = rowKey(r);
    seen.add(key);
    let tr = rowNodes.get(key);
    if (!tr) {
      tr = buildRow(key);
      rowNodes.set(key, tr);
    }
    updateRow(tr, r, folders.get(r.folder_id), r.folder_id !== lastFolder);
    lastFolder = r.folder_id;
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
  if (sec) sec.hidden = false;
}

// renderRowActions puts Edit and Stop on a folder's leading row only, and
// rebuilds them solely when the folder they belong to changes — this runs on
// every poll, and replacing a button under a pointer that is hovering it is
// how a click lands on nothing.
function renderRowActions(cell, folder, tr) {
  const want = folder && !readOnly ? folder.id : '';
  if (cell.dataset.for === want) return;
  cell.dataset.for = want;
  cell.replaceChildren();
  if (!want) return;
  const edit = el('button', 'small-btn', 'Edit');
  edit.title = 'Change what this folder excludes';
  edit.onclick = () => openIgnoreEditor(tr, folder, lastModel);
  cell.appendChild(edit);
  const stop = el('button', 'small-btn danger', 'Stop');
  stop.title = folder.snapshotted
    ? 'Stop one or both kinds of backup of this folder'
    : 'Stop backing up this folder. The copies already made are kept.';
  stop.onclick = () => (folder.snapshotted ? openStopChooser(tr, folder) : removeFolder(folder));
  cell.appendChild(stop);
}

// A folder with both kinds of backup needs to be asked WHICH to stop.
//
// There used to be one button, and it stopped the folder — taking the snapshot
// schedule with it. Somebody who wanted a daily snapshot, got an unwanted
// continuous mirror as well and pressed the only control on offer, silently
// lost the backup they actually wanted. A confirm() box cannot ask this: it has
// two answers and this question has three.
function openStopChooser(tr, f) {
  for (const open of tr.parentElement.querySelectorAll('tr.ignore-row')) open.remove();
  editingIgnoresFor = f.id;
  const row = el('tr', 'ignore-row');
  const cell = el('td');
  cell.colSpan = 6;
  row.appendChild(cell);

  const box = el('div', 'ignore-editor');
  box.appendChild(el('span', 'hint',
    `"${f.label}" has both kinds of backup: it is copied continuously AND sealed into a scheduled snapshot. ` +
    `Which do you want to stop? Nothing is deleted either way — the copies already made stay where they are.`));

  const mirrorOnly = el('button', 'small-btn', 'Stop copying continuously');
  mirrorOnly.title = 'The scheduled snapshot keeps running';
  mirrorOnly.onclick = async () => {
    mirrorOnly.disabled = true;
    const resp = await mutate('/api/folders/' + encodeURIComponent(f.id) + '/stop-mirror', { method: 'POST' });
    if (!resp.ok) { alert(await resp.text()); mirrorOnly.disabled = false; return; }
    editingIgnoresFor = null;
    refresh();
  };

  const everything = el('button', 'small-btn danger', 'Stop both');
  everything.title = 'Stop the continuous copy and the scheduled snapshot';
  everything.onclick = () => {
    editingIgnoresFor = null;
    removeFolder(f);
  };

  const cancel = el('button', 'small-btn quiet', 'Cancel');
  cancel.onclick = () => { editingIgnoresFor = null; refresh(); };

  box.append(mirrorOnly, everything, cancel);
  cell.appendChild(box);
  let after = tr;
  while (after.nextElementSibling && after.nextElementSibling.classList.contains('grouped')) {
    after = after.nextElementSibling;
  }
  after.after(row);
}

// Folders that are recorded here but that nothing is copying.
//
// SEPARATE FROM "No longer protected", which is a different situation with
// different actions. Those are folders REMOVED from the config, each carrying
// the destinations that still hold its copies — which is why that panel can
// offer "Turn back on" and the one deliberate "Delete backups…" in the program.
// A folder here is still configured and has no copies recorded anywhere, so
// both of those buttons would be aimed at nothing; pointing the deleting one at
// a live folder is exactly what the first rule of this codebase forbids.
//
// The two honest actions are: give it a backup, or stop listing it.
function renderUnprotected(st) {
  const sec = document.getElementById('unprotected-section');
  const list = document.getElementById('unprotected-list');
  if (!sec || !list) return;
  // `=== false` and not `!f.protected`: a daemon too old to send the field must
  // not have every folder declared unprotected.
  const bare = (st.folders || []).filter((f) => f.protected === false);
  if (bare.length === 0) { sec.hidden = true; return; }
  sec.hidden = false;
  list.replaceChildren();

  for (const f of bare) {
    const li = el('li', 'card');
    li.appendChild(icon('folder'));
    li.appendChild(el('strong', null, f.label));

    const meta = el('div', 'card-meta');
    if (f.path) meta.appendChild(el('span', 'muted mono', f.path));
    meta.appendChild(el('span', 'bad', 'no destination is mirroring it and no schedule is snapshotting it'));
    li.appendChild(meta);

    // The fact is shown on the read-only network view too — it is a health
    // fact, and the same reasoning that keeps space warnings there keeps this.
    // Only the buttons are withheld.
    if (!readOnly) {
      const actions = el('div', 'card-actions');
      const setUp = el('button', 'small-btn primary', 'Set up a backup');
      setUp.onclick = () => Wizard.open(st, { firstRun: false, folder: { id: f.id, path: f.path } });
      actions.appendChild(setUp);
      const drop = el('button', 'small-btn', 'Stop listing this folder');
      drop.onclick = () => removeFolder(f);
      actions.appendChild(drop);
      li.appendChild(actions);
    }
    list.appendChild(li);
  }
}

// Folders that were stopped, whose backups are still on the destinations.
//
// The card says where each copy actually is, because that is the whole reason
// the panel exists: without it those copies are named by nothing — not this
// config, not the destination's manifest, not the adopt flow.
function renderRetired(st) {
  const sec = document.getElementById('retired-section');
  const list = document.getElementById('retired-list');
  if (!sec || !list) return;
  const retired = st.retired || [];
  // Never on the read-only view — which is not even sent this — and never as
  // an empty panel explaining a situation nobody is in.
  if (readOnly || retired.length === 0) { sec.hidden = true; return; }
  sec.hidden = false;
  list.replaceChildren();

  for (const r of retired) {
    const li = el('li', 'card');
    li.appendChild(icon('folder'));
    li.appendChild(el('strong', null, r.label));

    const meta = el('div', 'card-meta');
    if (r.path) meta.appendChild(el('span', 'muted mono', r.path));
    if (r.stopped_at) meta.appendChild(el('span', 'muted', 'stopped ' + humanTime(r.stopped_at)));
    for (const c of r.copies || []) {
      // A failed delete attempt is reported against the destination it failed
      // on, so "some of it went" is visible rather than implied.
      if (c.error) {
        meta.appendChild(el('span', 'bad', c.target + ' — ' + c.error));
      } else if (c.removed) {
        meta.appendChild(el('span', 'muted', c.target + ' — deleted'));
      } else if (!c.deletable) {
        meta.appendChild(el('span', 'muted', c.target + ' — on another computer'));
      } else if (!c.still_configured) {
        meta.appendChild(el('span', 'muted', c.target + ' — no longer set up here'));
      } else {
        meta.appendChild(el('span', 'muted', 'copy on ' + c.target));
      }
    }
    li.appendChild(meta);

    const actions = el('div', 'card-actions');
    // Each action renders as a button OR as the daemon's reason for refusing
    // it — never a button that would be rejected on click. The reasons are
    // computed by the daemon (status.RetiredInfo) so the page and the refusal
    // cannot drift apart.
    if (r.reenable_blocked) {
      actions.appendChild(el('span', 'muted small', r.reenable_blocked));
    } else {
      const on = el('button', 'small-btn', 'Turn back on');
      on.onclick = () => reenableFolder(r);
      actions.appendChild(on);
    }
    const forget = el('button', 'small-btn', 'Forget this');
    forget.onclick = () => forgetRetired(r);
    actions.appendChild(forget);
    if (r.delete_blocked) {
      actions.appendChild(el('span', 'muted small', r.delete_blocked));
    } else {
      const del = el('button', 'small-btn danger', 'Delete backups…');
      del.onclick = () => deleteRetiredBackups(r, del);
      actions.appendChild(del);
    }
    li.appendChild(actions);
    list.appendChild(li);
  }
}

async function reenableFolder(r) {
  const where = (r.copies || []).filter((c) => c.still_configured).map((c) => c.target);
  if (!confirm(`Start backing up ${r.path} again?\n\n` +
    (where.length ? `It reconnects to ${where.join(', ')} and picks up from the backup already there — nothing is copied again from scratch.`
      : 'Nothing it used to back up to is still set up here, so you will need to choose a destination.'))) return;
  const resp = await mutate('/api/retired/' + encodeURIComponent(r.id) + '/reenable', { method: 'POST' });
  await reportAndRefresh(resp);
}

async function forgetRetired(r) {
  const where = (r.copies || []).map((c) => c.target).join(', ');
  if (!confirm(`Forget this record?\n\n` +
    (where ? `The backups on ${where} are left exactly where they are — ` : '') +
    `this only removes the reminder that they exist. Nothing on this page will mention them again.`)) return;
  const resp = await mutate('/api/retired/' + encodeURIComponent(r.id), { method: 'DELETE' });
  await reportAndRefresh(resp);
}

// The one action in this program that deletes a backup on purpose.
//
// TWO STEPS, AND THE SECOND IS TYPING THE NAME. "Are you sure" is answered yes
// by reflex; typing the folder's own label is not done by accident. The daemon
// checks the typed name again — the guarantee does not live in this file.
async function deleteRetiredBackups(r, btn) {
  const here = (r.copies || []).filter((c) => c.deletable && !c.removed);
  const elsewhere = (r.copies || []).filter((c) => !c.deletable).map((c) => c.target);
  const where = here
    .map((c) => `  • ${c.target}${c.location ? ' — ' + c.location : ''}${c.dest_path ? ' (' + c.dest_path + ')' : ''}`)
    .join('\n');

  if (!confirm(
    `PERMANENTLY DELETE the backups of "${r.label}"?\n\n` +
    `This deletes the backed-up copies themselves, and every saved previous version of them, from:\n\n` +
    `${where}\n\n` +
    `The folder on this computer (${r.path}) is NOT touched.\n\n` +
    (elsewhere.length
      ? `A copy also sits on ${elsewhere.join(', ')}, which is another computer. It cannot be deleted from here — do that on that machine.\n\n`
      : '') +
    ((r.archives && r.archives.length)
      ? `Timed snapshots are not touched. A snapshot is one encrypted zip that may hold other folders too, so there is no way to remove just this one from it.\n\n`
      : '') +
    `There is no undo. Once these are gone, the only copy of "${r.label}" is the folder on this computer.`)) return;

  const typed = prompt(`To confirm, type the folder's name exactly:\n\n${r.label}`);
  if (typed === null) return;
  if (typed.trim() !== r.label) { alert('That did not match. Nothing was deleted.'); return; }

  // A network drive can take a while, and the answer IS the value of the
  // button, so the wait is shown rather than hidden behind an optimistic
  // re-render.
  const was = btn.textContent;
  btn.disabled = true;
  btn.textContent = 'Deleting…';
  try {
    const resp = await mutate('/api/retired/' + encodeURIComponent(r.id) + '/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ confirm: r.label }),
    });
    await reportAndRefresh(resp);
  } finally {
    btn.disabled = false;
    btn.textContent = was;
  }
}

// Show what the daemon said, then re-read the model. Shared by the three
// stopped-folder actions because each of them answers with a sentence worth
// reading — "2 of 3 destinations cleared" is not something to swallow.
async function reportAndRefresh(resp) {
  let said = '';
  try {
    const body = await resp.clone().json();
    said = body && body.message ? body.message : '';
  } catch { said = (await resp.text()).trim(); }
  if (!resp.ok) alert(said || 'That did not work.');
  else if (said) alert(said);
  refresh();
}

// Inline editor for one folder's excludes. Comma-separated, matching the
// wizard, so the same list can be typed the same way in either place.
// What to leave out of a folder's backup.
//
// This used to be a bare text box with a placeholder and a paragraph of pattern
// syntax, opened by a button labelled only "Edit". Nothing said what was being
// edited. Worse, the box was EMPTY for every folder that had added no patterns
// of its own — while fifteen standard ones were quietly stripping hundreds of
// thousands of files from the backup. So it read as "nothing is excluded here",
// which was the opposite of the truth, and the one fact somebody opening this
// panel actually wants was the one thing missing from it.
function openIgnoreEditor(tr, f, st) {
  // The table is frozen while an editor is open, so the other rows' buttons
  // are still live: only ever show one box, or it isn't clear which folder
  // Save applies to.
  for (const open of tr.parentElement.querySelectorAll('tr.ignore-row')) open.remove();
  editingIgnoresFor = f.id;

  const row = el('tr', 'ignore-row');
  const cell = el('td');
  cell.colSpan = 6;
  row.appendChild(cell);

  const form = el('div', 'share-form');
  form.appendChild(el('h3', null, 'What to leave out of ' + (f.label || 'this folder')));

  // The standard list, named. It is doing most of the work on a typical
  // folder, and it was invisible.
  const std = (st && st.default_ignores) || [];
  if (std.length) {
    const box = el('div', 'std-ignores');
    box.appendChild(el('strong', null, 'Always skipped'));
    box.appendChild(el('div', 'muted mono small', std.join(', ')));
    form.appendChild(box);
  }

  // Whether that list applies at all — the same choice the wizard offers, in
  // the same words, and previously changeable only by hand-editing config.toml.
  const everythingLabel = el('label', 'muted');
  const everything = el('input');
  everything.type = 'checkbox';
  everything.checked = !!f.no_default_ignores;
  everythingLabel.append(everything, document.createTextNode(
    ' Include everything, even node_modules and build output'));
  form.appendChild(everythingLabel);
  form.appendChild(el('p', 'muted small',
    'Off by default. Ticking it backs up dependency and build folders too, ' +
    'which on a development folder is usually many times more data.'));

  form.appendChild(el('label', 'muted', 'Also leave out'));
  const input = el('input');
  input.type = 'text';
  input.value = (f.ignores || []).join(', ');
  input.placeholder = 'scratch, *.iso, assets/raw';
  form.appendChild(input);
  form.appendChild(el('p', 'muted small',
    'Separate patterns with commas. A name matches anywhere ("scratch", ' +
    '"*.iso"); a path matches that subfolder ("assets/raw"). Leave it empty to ' +
    'add nothing of your own.'));
  form.appendChild(el('p', 'muted small',
    'Changes take effect within a few seconds. Files that start matching stop ' +
    'being copied, and the copies already on your destinations are moved into ' +
    'version history — not deleted.'));

  const actions = el('div', 'card-actions');
  const save = el('button', 'small-btn primary', 'Save');
  const cancel = el('button', 'small-btn', 'Cancel');
  actions.append(save, cancel);
  form.appendChild(actions);

  const err = el('p', 'bad');
  err.hidden = true;
  form.appendChild(err);

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
      body: JSON.stringify({ ignores, no_default_ignores: everything.checked }),
    });
    if (!resp.ok) {
      err.hidden = false;
      setText(err, await resp.text());
      save.disabled = false;
      return;
    }
    close();
  };

  cell.appendChild(form);
  // After the folder's LAST destination row, so the editor sits under the
  // whole group rather than splitting it.
  let after = tr;
  while (after.nextElementSibling && after.nextElementSibling.classList.contains('grouped')) {
    after = after.nextElementSibling;
  }
  after.after(row);
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
  // Hidden when nothing is protected, not just when there are no destinations.
  // A destination that mirrors no folders is not "where backups go" — it is a
  // place nothing is being sent, and the panel described it as covering "every
  // folder" because an empty folder list means exactly that in the config.
  // Rather than explain a destination doing nothing, the section waits until
  // there is something for it to be about.
  //
  // A destination still holding a STOPPED folder's copies counts as something
  // to be about: "No longer protected" names those destinations, and hiding
  // the panel that describes them would leave that section referring to
  // somewhere the page refuses to show.
  const nothingProtected = (st.folders || []).length === 0;
  const nothingStopped = (st.retired || []).length === 0;
  sec.hidden = targets.length === 0 || (nothingProtected && nothingStopped);
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
    const dstate = el('span', stateClass(t.state) + ' dot', stateLabel(t.state));
    dstate.title = stateHelp(t.state);
    meta.appendChild(dstate);
    // Said once, in words. "0 folder(s)" repeated what the state beside it
    // already says, and the "(s)" was there because the count was rendered
    // without ever looking at it.
    if (t.all_folders) {
      meta.appendChild(el('span', 'muted', 'every folder'));
    } else if (t.folder_count > 0) {
      meta.appendChild(el('span', 'muted',
        t.folder_count === 1 ? '1 folder' : `${t.folder_count} folders`));
    }
    if (t.wake_enabled) meta.appendChild(el('span', 'muted', 'wakes on demand'));
    // Deleting a user's backup history is never silent.
    if (t.reclaim_note) meta.appendChild(el('span', 'busy', t.reclaim_note));
    li.appendChild(meta);
    // A name clash is data loss averted, not a detail: it gets a sentence under
    // the destination rather than only a tooltip, because the remedy is not
    // guessable and a red dot alone sends somebody hunting the wrong fault.
    if (t.state === 'name-clash') {
      li.appendChild(el('p', 'hint bad', stateHelp(t.state)));
    }
    const actions = el('div', 'card-actions');
    if (!readOnly) {
      const btn = el('button', 'small-btn danger', 'Remove');
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
  // Says where it goes, not just what stops. The old wording promised the
  // backups were left alone and was true — but the folder then vanished from
  // the page entirely, so "left exactly where they are" described files
  // nothing on screen could reach any more.
  if (!confirm(`Stop backing up ${f.path}?\n\n` +
    `It moves to "No longer protected" below, where you can turn it back on or delete its backups later.\n\n` +
    `Nothing is deleted now — the copies already on your destinations are left exactly where they are.`)) return;
  const resp = await mutate('/api/folders/' + encodeURIComponent(f.id), { method: 'DELETE' });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

async function removeTarget(t) {
  if (!confirm(`Stop backing up to ${t.name}?\n\nThe backups already on it are left exactly where they are — this only stops future copying.`)) return;
  const resp = await mutate('/api/targets/' + encodeURIComponent(t.name), { method: 'DELETE' });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

function renderArchives(st) {
  if (editingArchive !== null) return;
  const sec = document.getElementById('archives-section');
  if (!st.archives || st.archives.length === 0) { sec.hidden = true; return; }
  sec.hidden = false;
  const body = document.querySelector('#archive-rows tbody');
  body.replaceChildren();
  for (const a of st.archives) {
    const tr = el('tr');
    // WHAT is snapshotted, first. A job's name is whatever somebody typed in
    // an optional box — it can say anything, and on a real machine it said
    // something that read like the right folder while the job covered a
    // different one. The folder is the fact worth checking.
    const stopped = a.stopped_folders || [];
    const covers = a.covers_everything
      ? 'every folder'
      : (a.folders && a.folders.length
        ? a.folders.join(', ')
        : (stopped.length
          ? `nothing — ${stopped.map(s => s.label).join(', ')} is stopped`
          : 'nothing — its folder is stopped'));
    // Built by name rather than by index. The state cell used to be found by
    // counting to 3, so adding this Folder column silently moved the colouring
    // and the "enter password…" button onto the schedule instead — a column
    // added a year from now would do it again.
    const archiveLabels = ['Folder', 'Name', 'Destination', 'Every'];
    [covers, a.name, a.target, a.every].forEach((text, i) => {
      const td = el('td', undefined, text);
      td.dataset.label = archiveLabels[i];
      tr.appendChild(td);
    });
    tr.dataset.folder = covers;
    const state = el('td');
    const stateText = el('span', undefined, archiveStateLabel(a));
    state.appendChild(stateText);
    // Anything that is working or about to work is "busy", never "bad". A new
    // state added without a case here inherits red, which is how "running"
    // would have been drawn as a fault.
    // Green once a zip exists, including while the next one is being written:
    // the dot answers the same question the word does, and amber beside
    // "backed up" would take the reassurance straight back.
    // ' dot' to match the mirror table: the same word in the same colour
    // deserves the same marker beside it, and without it the two tables read
    // as describing different kinds of thing.
    stateText.className = (a.paused ? 'paused'
      : (['ok', 'due'].includes(a.state) || (['running', 'preparing'].includes(a.state) && a.last_run)) ? 'ok'
      : (['never run', 'preparing', 'running'].includes(a.state) || a.needs_password ? 'busy' : 'bad')) + ' dot';
    // A snapshot with no stored password never runs; offer to set it here
    // rather than sending the user to the command line.
    if (a.needs_password && !readOnly) {
      const btn = el('button', 'small-btn', 'enter password…');
      btn.onclick = () => enterArchivePassword(a);
      state.appendChild(btn);
    }
    state.dataset.label = 'State';
    tr.appendChild(state);
    const prog = archiveProgressCell(a);
    prog.dataset.label = 'Progress';
    tr.appendChild(prog);
    const last = el('td', undefined, humanTime(a.last_run));
    last.dataset.label = 'Last run';
    tr.appendChild(last);

    // Pause, edit, stop. Without these the only way to change a schedule was
    // to delete it and build it again — including retyping a password that by
    // design cannot be recovered — and there was no way to stop one at all.
    const actions = el('td', 'row-actions');
    if (!readOnly) {
      // A schedule covering nothing cannot run, and Pause / Change / Stop are
      // all answers to a different question. Offer the one thing that fixes
      // it, first, so the row says what to do rather than only what is wrong.
      for (const s of stopped) {
        // The real retired record, not a stub: reenableFolder says which
        // destinations it will reconnect to, and it can only do that from the
        // record that lists them.
        const rec = (st.retired || []).find((r) => r.id === s.id);
        if (!rec) continue;
        const back = el('button', 'small-btn', `Turn ${s.label} back on`);
        back.title = 'Start backing this folder up again, so this schedule can run';
        back.onclick = () => reenableFolder(rec);
        actions.appendChild(back);
      }

      const pause = el('button', 'small-btn', a.paused ? 'Resume' : 'Pause');
      pause.onclick = () => setArchivePaused(a, !a.paused);
      actions.appendChild(pause);

      // "Edit", the same word the mirror table uses for the same idea. It used
      // to say "Change…" and open two browser prompt() boxes.
      const edit = el('button', 'small-btn', 'Edit');
      edit.onclick = () => openArchiveEditor(tr, a);
      actions.appendChild(edit);

      const stop = el('button', 'danger small-btn', 'Stop');
      stop.onclick = () => removeArchive(a);
      actions.appendChild(stop);
    }
    tr.appendChild(actions);
    // Files that were being written while the snapshot read them. Said on the
    // row rather than buried in a tooltip, because a zip is sealed for ever:
    // unlike the mirror, which recopies such a file on its next pass, this one
    // keeps the torn copy until the snapshot is deleted.
    if (a.unstable && a.unstable.length) {
      const warn = el('tr', 'ignore-row');
      const cell = el('td');
      cell.colSpan = 8;
      const n = a.unstable.length;
      cell.appendChild(el('span', 'bad',
        `${n} file${n === 1 ? ' was' : 's were'} being written while this snapshot read ${n === 1 ? 'it' : 'them'}, so ${n === 1 ? 'its copy' : 'their copies'} inside the zip may be incomplete:`));
      cell.appendChild(el('div', 'muted mono small', a.unstable.join(', ')));
      cell.appendChild(el('div', 'muted small',
        'The next run will copy them cleanly if nothing is writing to them at the time.'));
      warn.appendChild(cell);
      body.appendChild(tr);
      body.appendChild(warn);
      if (a.detail) tr.title = a.detail;
      continue;
    }
    if (a.detail) tr.title = a.detail;
    body.appendChild(tr);
  }
}

// The state column answers the same question the mirror's does — are the files
// safe — in the same words. Two tables on one page describing two kinds of
// backup had no business using two vocabularies, and "ok" is the engine's word
// rather than an answer to anything a person asked.
function archiveStateLabel(a) {
  switch (a.state) {
    case 'ok':
    case 'due':
      // Due means the NEXT snapshot is owed, not that the last one is missing.
      return 'backed up';
    case 'running':
    case 'preparing':
      // A first zip does not exist yet and must not be claimed; a second is
      // being written while the first sits safely on the destination.
      return a.last_run ? 'backed up' : 'first backup';
    case 'never run':
      return 'not backed up yet';
    default:
      return a.state; // failed, needs password, paused
  }
}

// How far the running snapshot has got, drawn the way a mirror row draws it.
//
// A snapshot of a real folder takes tens of minutes, and this row said only
// "running" for the whole of it — the same word at one minute and at
// twenty-nine. Bytes are SOURCE bytes: the zip is compressed by a ratio nobody
// knows in advance, so a bar measured against the file landing on the
// destination would fill early and then sit still.
function archiveProgressCell(a) {
  const cell = el('td', 'progress-cell');
  const running = a.total_bytes > 0 || a.state === 'running' || a.state === 'preparing';
  if (!running) {
    // Not a bar at 0%: an idle schedule is not a stalled one.
    cell.appendChild(el('span', 'muted', '—'));
    return cell;
  }
  const wrap = el('div', 'progress-wrap');
  const bar = el('div', 'bar');
  const fill = el('div', 'bar-fill busy');
  const pct = a.total_bytes > 0
    ? Math.max(0, Math.min(100, ((a.done_bytes || 0) / a.total_bytes) * 100))
    : 0;
  // Before the pre-pass finishes there is no denominator, and stripes say
  // "working" without inventing one.
  if (a.total_bytes > 0) {
    fill.style.width = pct + '%';
  } else {
    fill.classList.add('indeterminate');
    fill.style.width = '100%';
  }
  bar.appendChild(fill);
  const label = el('span', 'bar-label');
  // Naming the phase matters most at the end: packing finishes, the bar sits
  // full, and verification then re-reads the whole archive off the destination
  // for minutes. Without a word for that, a working job looks like a hung one.
  const what = a.phase === 'verifying' ? 'checking it opens: ' : '';
  setText(label, a.total_bytes > 0
    ? `${what}${humanBytes(a.done_bytes || 0)} of ${humanBytes(a.total_bytes)}`
    : 'counting…');
  wrap.append(bar, label);
  cell.appendChild(wrap);
  if (a.total_files > 0) {
    cell.title = `${(a.done_files || 0).toLocaleString()} of ${a.total_files.toLocaleString()} files`;
  }
  return cell;
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
      const btn = el('button', 'primary', 'Approve');
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
  //
  // ONLY ON ARRIVAL, NEVER MID-SESSION. A wizard that covers the whole page is
  // right when there is nothing behind it and an interruption when there is:
  // this used to fire the moment somebody removed their last folder, taking
  // the dashboard away from them in the middle of using it. The daemon-side
  // fix is to persist that setup happened (see config.Configured), and this is
  // the belt to that braces — it also holds on a machine whose state.json
  // cannot be written at all.
  if (!readOnly && !paintedOnce && !st.setup_complete && !Wizard.isOpen() && !AdoptWizard.isOpen()) {
    Wizard.open(st, { firstRun: true });
  }
  paintedOnce = true;
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
  renderLANAddress(st);
  renderVerdict(st);
  renderUnprotected(st);
  renderRows(st);
  renderRetired(st);
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
  // The documentation is served only by the loopback dashboard — /docs is not
  // on the network view's allow-list — so the link would 404 from here. A link
  // that plainly does not work is worse than no link.
  const docs = document.getElementById('docs-link');
  if (docs) docs.hidden = true;
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
// The address another device can open this status page at.
//
// It lived only in the Security panel, three clicks down, described in a
// sentence — so the answer to "what do I type into my phone" was somewhere you
// had to already know to look. The header is where that belongs.
//
// SHOWN ONLY WHILE THE VIEW IS ACTUALLY LISTENING. This machine always has a
// LAN address; what matters is whether anything is answering on it. Printing
// one that nothing serves would be a promise the program cannot keep, and the
// person holding the phone would be left wondering which of the two ends is
// broken.
function renderLANAddress(st) {
  const el2 = document.getElementById('lan-address');
  if (!el2) return;
  const url = st.settings && st.settings.lan_view && st.settings.lan_view_url;
  if (!url) { el2.hidden = true; return; }
  el2.hidden = false;
  el2.href = url;
  el2.target = '_blank';
  el2.rel = 'noopener';
  // Host and port only: the scheme is noise on a chip this small, and it is
  // what somebody types into a phone.
  // Labelled, because a bare IP in a header is a riddle: it could be this
  // machine, a destination, or a device waiting to pair.
  setText(el2, 'Status: ' + String(url).replace(/^https?:\/\//, ''));
  el2.title = 'Open this page from another device on your network at ' + url;
}

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

  const folders = st.folders || [];
  const unprotected = folders.filter((f) => f.protected === false);

  let state = 'ok';
  let text;
  // BOTH HALVES, OR THE ANSWER IS VACUOUS. This asked only whether a
  // destination existed, so a machine with a destination and no folders fell
  // through to "Everything is backed up" — which is true the way an empty list
  // satisfies any claim, and a lie in the only sense that matters. It is what
  // the page said the moment somebody stopped protecting their last folder.
  if (targets.length === 0 || folders.length === 0) {
    state = '';
    // "yet" is right for a machine that has never backed anything up and wrong
    // for one that has just been emptied, so it is only claimed when nothing
    // has been set up at all.
    text = targets.length === 0 && folders.length === 0
      ? 'Nothing is being backed up yet'
      : 'Nothing is being backed up';
  } else if (unprotected.length > 0) {
    // Ahead of a broken destination on purpose. A destination that needs
    // attention is a backup at risk; a folder backed up by nothing is a backup
    // that does not exist, and the page said "Everything is backed up" while
    // one sat there.
    state = 'bad';
    text = unprotected.length === 1
      ? `${unprotected[0].label} is not backed up by anything`
      : `${unprotected.length} folders are not backed up by anything`;
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
  ['set-update-check', 'update_check'],
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
  savedState('webhook-url-state', st.settings.webhook_url_set,
    st.settings.webhook_url_display,
    'No address saved yet — alerts have nowhere to go until you save one.');

  // ntfy, the same way. Its topic is treated as a credential like the webhook
  // address — on the public server the topic name is the only thing between a
  // stranger and every alert this machine sends — so the panel is told that one
  // exists and never what it is.
  const ntfyBox = document.getElementById('ntfy-config');
  if (ntfyBox) ntfyBox.hidden = !st.settings.ntfy_enabled;
  savedState('ntfy-topic-state', st.settings.ntfy_topic_set,
    st.settings.ntfy_topic_display,
    'No topic saved yet — alerts have nowhere to go until you save one.');
  // A missing token is perfectly normal — a public ntfy.sh topic needs none —
  // so this one never renders as a fault, and there is nothing safe to echo:
  // a token is secret end to end, with no host half to show.
  savedState('ntfy-token-state', st.settings.ntfy_token_set, '',
    'No token saved. A public topic does not need one.', true);

  // Whether a newer release exists. Stated here as well as announced once,
  // because an announcement can be missed and this page is the thing people
  // come back to.
  const upd = document.getElementById('update-state');
  if (upd) {
    upd.hidden = !st.settings.update_check;
    if (st.settings.update_available) {
      upd.textContent = 'backup-maker ' + st.settings.update_available.replace(/^v/, '') +
        ' is available. Your backups are fine — upgrade when convenient.';
      upd.className = 'small ok';
    } else if (!st.settings.update_comparable) {
      // A build from source has no release version, so nothing is ever asked
      // and nothing can be compared. Saying "you are up to date" here would be
      // a reassurance nobody earned.
      upd.textContent = 'This build has no release version, so there is nothing to compare it with.';
      upd.className = 'muted small';
    } else if (!st.settings.update_checked_at) {
      upd.textContent = 'Nothing checked yet — the first check runs a few minutes after startup.';
      upd.className = 'muted small';
    } else {
      upd.textContent = 'You are on the newest release (checked ' +
        humanTime(st.settings.update_checked_at) + ').';
      upd.className = 'muted small';
    }
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

  renderLANBanner(st, mode);
  renderLANRequests(st);
}

// The network view's address, at the top of the page.
//
// THE SETTINGS PANEL IS THE WRONG PLACE FOR THIS AND ONLY THIS. Everything else
// in there is something you set once; the address is something you READ, off the
// screen, to type into a phone standing in another room — and since the settings
// groups collapse by default it had become two clicks and a scroll away.
//
// Who may open it is shown beside it on purpose. "Anyone on this network can
// read this" is a fact about what this machine exposes, and it was invisible
// unless somebody went looking for the setting that caused it.
function renderLANBanner(st, mode) {
  const box = document.getElementById('lan-banner');
  if (!box) return;
  // Never on the read-only view itself: it is stripped of the settings block, so
  // there is nothing to render, and a view does not need to advertise its own
  // address back to the person already reading it.
  if (readOnly || !st.settings || !st.settings.lan_view) {
    box.hidden = true;
    return;
  }

  const link = document.getElementById('lan-banner-url');
  const access = document.getElementById('lan-banner-access');
  const url = st.settings.lan_view_url;

  if (!url) {
    // Switched on and not listening. The daemon's own reason, never a guess —
    // the same rule the settings panel follows, so the two cannot disagree.
    box.hidden = false;
    link.hidden = true;
    access.textContent = 'Switched on, but not listening: ' +
      (st.settings.lan_view_error || 'the reason was not reported.');
    access.className = 'small bad';
    return;
  }

  box.hidden = false;
  link.hidden = false;
  link.textContent = url;
  link.href = url;
  access.textContent = mode === 'approved'
    ? 'Only approved devices'
    : 'Anyone on this network';
  access.className = mode === 'approved' ? 'small muted' : 'small bad';
}

// Devices asking to watch backup status, answered from the top of the page.
//
// WHY NOT JUST A COUNT WITH A LINK. Somebody is standing in the room holding a
// phone showing a code, waiting on the person at the computer. Sending that
// person into a settings panel and asking them to expand a group first is the
// wrong shape for something with a human waiting on it. The code and both
// answers belong on screen the moment the request arrives.
//
// Requests lapse after five minutes (see pendingLANDeviceTTL), so what is on
// screen is always a device asking NOW — never a stale code somebody might
// approve without knowing what it is.
function renderLANRequests(st) {
  const sec = document.getElementById('lan-requests-section');
  const box = document.getElementById('lan-requests');
  if (!sec || !box) return;
  const waiting = readOnly ? [] : ((st.settings && st.settings.lan_devices) || [])
    .filter((d) => !d.approved);
  if (waiting.length === 0) {
    sec.hidden = true;
    box.replaceChildren();
    return;
  }
  sec.hidden = false;
  box.replaceChildren();

  const title = el('p', null);
  title.appendChild(el('strong', null, waiting.length === 1
    ? 'A device wants to see your backup status'
    : `${waiting.length} devices want to see your backup status`));
  box.appendChild(title);

  for (const d of waiting) {
    const row = el('div', 'lan-request');
    // The code is the whole point: it is what makes this a comparison against
    // the screen in your hand rather than a guess.
    row.appendChild(el('strong', 'mono lan-request-code', d.code));
    const meta = el('span', 'muted small');
    meta.textContent = (d.kind || 'a device') + (d.addr ? ' at ' + d.addr : '') +
      ' — asked ' + humanTime(d.first_seen);
    row.appendChild(meta);
    const actions = el('div', 'lan-request-actions');
    const ok = el('button', 'primary', 'Approve');
    ok.onclick = () => lanDeviceAction(d.code, 'approve', ok);
    const no = el('button', 'danger', 'Deny');
    no.onclick = () => lanDeviceAction(d.code, 'forget', no);
    actions.append(ok, no);
    row.appendChild(actions);
    box.appendChild(row);
  }

  const note = el('p', 'muted small');
  note.textContent = 'Check the code matches the one on that device before approving. ' +
    'Unanswered requests expire after 5 minutes.';
  box.appendChild(note);
}

// justSaved marks a credential field that was saved in the last few seconds, so
// its own line can confirm it.
//
// THE BUG THIS FIXES, reported twice before it was understood. Saving cleared
// the box (deliberately — a credential left in a form field ends up in a
// screenshot or the wrong autofill entry), the line underneath said "A topic is
// saved" both BEFORE and AFTER, so nothing about the panel changed, and the only
// confirmation rendered ~600px further down the page, off-screen at the moment
// of clicking. Three things at once, each of which alone reads as "it didn't
// save". The user's words: "it just resets and doesn't say save confirmed".
const justSaved = {};

// savedState renders one credential line: whether something is stored, WHICH
// address it is (host only — see notify.RedactEndpoint), and a confirmation
// right here for a few seconds after a save.
function savedState(id, isSet, display, emptyText, emptyIsFine) {
  const el = document.getElementById(id);
  if (!el) return;
  const fresh = justSaved[id] && Date.now() - justSaved[id] < 6000;
  if (!isSet) {
    // A save that CLEARED the field is still a save, and saying so matters:
    // removing an address on purpose should not look like a failure either.
    el.textContent = fresh ? '✓ Removed. ' + emptyText : emptyText;
    el.className = emptyIsFine || fresh ? 'muted small' : 'small bad';
    return;
  }
  const which = display ? ' — ' + display : '';
  el.textContent = fresh
    ? '✓ Saved' + which + '. Check the address is right; type a new one to replace it.'
    : 'Saved' + which + '. Type a new one to replace it, or save an empty box to remove it.';
  el.className = fresh ? 'small ok' : 'muted small';
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
      const ok = el('button', 'primary', 'Approve');
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
function wireCredentialSave(buttonId, inputId, key, savedMsg, stateId) {
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
        // Confirm AT THE CONTROL, not only in the page-level message line —
        // that one renders far enough down the section to be off-screen at the
        // moment the button is pressed.
        justSaved[stateId] = Date.now();
        // And repaint the line now rather than waiting for refresh(), so the
        // confirmation is on screen before the round trip finishes.
        if (lastModel) renderSettings(lastModel);
      }
    } catch {
      settingsMessage('Could not reach the backup daemon.', true);
    } finally {
      btn.disabled = false;
      refresh();
    }
  });
}

wireCredentialSave('save-webhook-url', 'set-webhook-url', 'webhook_url', 'Address saved.', 'webhook-url-state');
wireCredentialSave('save-ntfy-topic', 'set-ntfy-topic', 'ntfy_topic_url', 'Topic saved.', 'ntfy-topic-state');
wireCredentialSave('save-ntfy-token', 'set-ntfy-token', 'ntfy_token', 'Token saved.', 'ntfy-token-state');

// How each delivery method last performed, shown under that method.
//
// This is the one fault backup-maker cannot report by alerting about it: if the
// route the alert would travel is broken, the alert never arrives to say so. So
// it is stated on the page instead, next to the switch that turned it on.
// A fault that must stay visible while its group is shut.
//
// THE REASON THIS EXISTS. The delivery lines below say when a method last
// failed, and that is the one failure backup-maker cannot announce by alerting
// about it — the broken thing IS the route an alert would travel. Putting the
// settings behind a collapsed group would have buried it completely, so the
// summary carries the marker: a group nobody opens still shows that alerts are
// not getting out.
function renderGroupWarning(failed) {
  const warn = document.getElementById('alerts-group-warn');
  if (!warn) return;
  if (failed.length === 0) {
    warn.hidden = true;
    warn.textContent = '';
    return;
  }
  warn.hidden = false;
  warn.textContent = failed.length === 1
    ? `⚠ ${failed[0]} alerts are not being delivered`
    : `⚠ ${failed.length} delivery methods are failing`;
}

function renderDeliveryState(st) {
  const rows = (st.settings && st.settings.delivery) || [];
  const by = {};
  for (const r of rows) by[r.method] = r;
  renderGroupWarning(rows.filter((r) => r.at && !r.ok).map((r) => r.method));
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

// --- scheduled snapshots: pause, change, stop --------------------------

async function setArchivePaused(a, paused) {
  const resp = await mutate('/api/archives/' + encodeURIComponent(a.name) + '/paused', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ paused }),
  });
  if (!resp.ok) alert(await resp.text()); else refresh();
}

// Changing how often it runs and how many are kept. Deliberately does NOT
// touch the password or the folder: the password cannot be shown back (it is
// never sent to the browser), and re-pointing a schedule at a different folder
// is how somebody ends up with a snapshot of the wrong thing — that is a new
// schedule, made deliberately.
// Change a snapshot schedule, in a form built from the wizard's own controls
// and filled in with what the job is set to now.
//
// It used to be two browser prompt() boxes: one asking you to TYPE "hourly,
// 12h, daily, or weekly" into a text field with no list of the options and no
// spell-check, the second asking for a number. A prompt() cannot show you the
// current value, cannot validate, cannot be cancelled halfway without losing
// the first answer, and looks like a page that has gone wrong. The wizard asks
// these exact questions with a select and a number field; there was no reason
// changing an answer should be a worse experience than giving it.
function openArchiveEditor(tr, a) {
  for (const open of tr.parentElement.querySelectorAll('tr.ignore-row')) open.remove();
  editingArchive = a.name;

  const row = el('tr', 'ignore-row');
  const cell = el('td');
  cell.colSpan = 8;
  row.appendChild(cell);

  const form = el('div', 'share-form');
  form.appendChild(el('label', 'muted', 'Run a snapshot'));
  const every = el('select');
  for (const [value, text] of [['hourly', 'every hour'], ['12h', 'every 12 hours'],
    ['daily', 'every day'], ['weekly', 'every week']]) {
    const opt = el('option', null, text);
    opt.value = value;
    // Prepopulated — the whole point. The old prompt() offered the current
    // value as default text you had to retype correctly to keep.
    if (a.every === value) opt.selected = true;
    every.appendChild(opt);
  }
  form.appendChild(every);

  form.appendChild(el('label', 'muted', 'Keep this many snapshots (older ones are deleted)'));
  const keep = el('input');
  keep.type = 'number';
  keep.min = '1';
  keep.value = String(a.keep || 5);
  form.appendChild(keep);

  form.appendChild(el('label', 'muted', 'New password (leave blank to keep the current one)'));
  const pw = el('input');
  pw.type = 'password';
  pw.placeholder = 'unchanged';
  pw.autocomplete = 'new-password';
  const pw2 = el('input');
  pw2.type = 'password';
  pw2.placeholder = 'repeat new password';
  pw2.autocomplete = 'new-password';
  form.append(pw, pw2);
  // A password change does not re-encrypt what is already written, and that is
  // the one thing somebody would assume it does.
  form.appendChild(el('p', 'muted small',
    'Changing it affects snapshots written from now on. The zips already on ' +
    'your destination still open with the password that made them — nothing ' +
    'is re-encrypted, and nothing already written becomes unreadable.'));

  const actions = el('div', 'card-actions');
  const save = el('button', 'small-btn primary', 'Save');
  const cancel = el('button', 'small-btn', 'Cancel');
  actions.append(save, cancel);

  const err = el('p', 'bad');
  err.hidden = true;

  const close = () => { editingArchive = null; refresh(); };
  cancel.onclick = close;
  save.onclick = async () => {
    const n = parseInt(keep.value, 10);
    if (!Number.isFinite(n) || n < 1) {
      err.hidden = false;
      setText(err, 'Keep must be a whole number, 1 or more.');
      return;
    }
    if (pw.value !== pw2.value) {
      err.hidden = false;
      setText(err, 'The two passwords do not match.');
      return;
    }
    save.disabled = true;
    const name = encodeURIComponent(a.name);
    let resp = await mutate('/api/archives/' + name + '/schedule', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ every: every.value, keep: n }),
    });
    if (resp.ok && pw.value !== '') {
      resp = await mutate('/api/archives/' + name + '/password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pw.value }),
      });
    }
    if (!resp.ok) {
      err.hidden = false;
      setText(err, await resp.text());
      save.disabled = false;
      return;
    }
    close();
  };

  form.append(actions, err);
  cell.appendChild(form);
  tr.after(row);
  every.focus();
}

// "Stop" rather than "Delete": it stops the schedule running, and the
// snapshots it already wrote are left where they are. Saying "delete" would
// promise a file removal this does not do — and the zips stay openable with
// the password that made them.
async function removeArchive(a) {
  if (!confirm(
    `Stop the schedule "${a.name}"?\n\n` +
    `It will not run again.\n\n` +
    `The snapshots it has already written are left exactly where they are, and still ` +
    `open with the password that made them. Its stored password is forgotten, so ` +
    `keep your own copy of it if you may need those snapshots.`)) return;
  const resp = await mutate('/api/archives/' + encodeURIComponent(a.name), { method: 'DELETE' });
  await reportAndRefresh(resp);
}
