// SPDX-License-Identifier: MIT

// The browser setup wizard: one backup per run — choose a folder, choose where
// copies go, optionally schedule snapshots, confirm.
//
// It deliberately does not ask the user to know the difference between a local
// drive, an SMB share and a paired machine. Step 2 shows computers; you drill
// into one to see what storage it offers.
const Wizard = (() => {
  const $ = (id) => document.getElementById(id);

  // The wizard is the ONLY way to set up a backup, so it has to explain
  // itself as it goes: each step's heading is the question being asked.
  const STEPS = {
    kind: {
      chip: "Kind",
      title: "What kind of backup is this?",
      lede: "You can set up the other kind afterwards — run this again any time.",
    },
    folder: {
      chip: "Folder",
      title: "Which folder should be protected?",
      lede: "Everything inside it is included. Junk like node_modules is skipped automatically.",
    },
    dest: {
      chip: "Destinations",
      title: "Where should the copies go?",
      lede: "Pick as many places as you like — more copies, in more places, is the whole point. Click a computer to see what storage it offers.",
    },
    schedule: {
      chip: "Schedule",
      title: "How often, and what password?",
      lede: "Each run writes one encrypted zip. Nothing is copied in between.",
    },
    review: {
      chip: "Review",
      title: "Ready to start",
      lede: "Nothing has been saved yet. If any destination can't be reached, none of this is written.",
    },
  };

  let order = [];
  let index = 0;
  let firstRun = false;
  let model = null;
  let chosenFolder = "";
  let chosenFolderID = "";   // set when reusing an already-protected folder
  const chosen = new Map();

  function mode() {
    const el = document.querySelector('input[name="mode"]:checked');
    return el ? el.value : "incremental";
  }

  // A timed backup needs a schedule; an incremental one has nothing to
  // schedule, so that step simply doesn't exist for it.
  function rebuildOrder() {
    const keep = order[index];
    order = mode() === "timed"
      ? ["kind", "folder", "dest", "schedule", "review"]
      : ["kind", "folder", "dest", "review"];
    const at = order.indexOf(keep);
    index = at >= 0 ? at : Math.min(index, order.length - 1);
  }

  function isOpen() { return !$("wizard").hidden; }

  function open(st, opts) {
    model = st || null;
    firstRun = !!(opts && opts.firstRun);
    index = 0;
    chosenFolder = "";
    chosenFolderID = "";
    chosen.clear();
    const inc = document.querySelector('input[name="mode"][value="incremental"]');
    if (inc) inc.checked = true;
    rebuildOrder();
    $("wiz-error").hidden = true;
    $("pick-chosen").hidden = true;
    $("dest-count").hidden = true;
    // A previous run may have ended on the waiting-to-pair panel, which
    // replaces the steps rather than being one of them.
    stopPairWait();
    $("wiz-pairwait").hidden = true;
    $("wiz-nav").hidden = false;
    $("dest-paired").replaceChildren();
    $("wizard").hidden = false;
    $("dashboard").hidden = true;
    $("wiz-skip").hidden = !firstRun;
    // "Restore this machine" only makes sense on a fresh machine: once a folder
    // or a target exists, adoption is refused server-side anyway.
    const empty = model && (model.folders || []).length === 0 && (model.targets || []).length === 0;
    $("kind-adopt").hidden = !empty;
    renderExistingFolders();
    loadRoots();
    loadMachines(false);
    render();
  }

  function close() {
    stopPairWait();
    $("wizard").hidden = true;
    $("dashboard").hidden = false;
  }

  function render() {
    const id = order[index];
    const step = STEPS[id];

    document.querySelectorAll(".wiz-panel").forEach((p) => {
      p.hidden = p.dataset.step !== id;
    });
    $("wiz-title").textContent = step.title;
    $("wiz-lede").textContent = step.lede;
    $("wiz-context").textContent = firstRun
      ? `Set up your first backup · step ${index + 1} of ${order.length}`
      : `Set up a backup · step ${index + 1} of ${order.length}`;

    const prog = $("wiz-progress");
    prog.replaceChildren();
    order.forEach((sid, i) => {
      const li = document.createElement("li");
      li.textContent = STEPS[sid].chip;
      if (i === index) li.className = "current";
      else if (i < index) li.className = "done";
      prog.appendChild(li);
    });

    $("wiz-back").disabled = index === 0;
    const last = index === order.length - 1;
    $("wiz-next").hidden = last;
    $("wiz-finish").hidden = !last;
    if (id === "review") renderReview();
  }

  function canAdvance() {
    switch (order[index]) {
      case "folder":
        if (!chosenFolder) return "Choose a folder first.";
        break;
      case "dest":
        if (chosen.size === 0) return "Choose at least one place to back up to.";
        break;
      case "schedule":
        if (!$("arc-pass").value) return "Snapshots need a password.";
        if ($("arc-pass").value !== $("arc-pass2").value) return "The two passwords do not match.";
        break;
    }
    return "";
  }

  // --- step 1: folder picker -------------------------------------------

  async function loadRoots() {
    const resp = await fetch('/api/browse');
    if (!resp.ok) return;
    const data = await resp.json();
    const list = $('pick-list');
    list.replaceChildren();
    $('pick-path').textContent = '';
    $('pick-up').disabled = true;
    for (const r of data.roots || []) addPickerEntry(list, r, true);
  }

  async function loadDir(path) {
    const resp = await fetch('/api/browse?path=' + encodeURIComponent(path));
    if (!resp.ok) { alert(await resp.text()); return; }
    const data = await resp.json();
    $('pick-path').textContent = data.path;
    $('pick-up').disabled = !data.parent;
    $('pick-up').dataset.parent = data.parent || '';
    $('pick-truncated').hidden = !data.truncated;
    const list = $('pick-list');
    list.replaceChildren();
    if (!data.entries || data.entries.length === 0) {
      list.appendChild(mk('li', 'muted', 'No sub-folders in here.'));
    }
    for (const e of data.entries || []) addPickerEntry(list, e, false);
  }

  function addPickerEntry(list, entry, isRoot) {
    const li = mk('li');
    const openBtn = mk('button', 'link', (isRoot ? '' : '📁 ') + entry.name);
    openBtn.onclick = () => loadDir(entry.path);
    const useBtn = mk('button', null, 'Protect this');
    useBtn.onclick = () => chooseFolder(entry.path);
    li.append(openBtn, useBtn);
    list.appendChild(li);
  }

  // Offer folders already under protection, so a second kind of backup can be
  // attached to one instead of failing on the duplicate-path guard.
  function renderExistingFolders() {
    const box = $("existing-folders");
    const list = $("existing-folder-list");
    const folders = (model && model.folders) || [];
    box.hidden = folders.length === 0;
    list.replaceChildren();
    for (const f of folders) {
      const li = mk("li", "card");
      li.appendChild(mk("strong", null, f.label));
      li.appendChild(mk("span", "muted mono", f.path));
      const btn = mk("button", null, "Add a backup for this");
      btn.onclick = () => chooseFolder(f.path, f.id);
      li.appendChild(btn);
      list.appendChild(li);
    }
  }

  function chooseFolder(path, folderID) {
    chosenFolderID = folderID || "";
    chosenFolder = path;
    const p = $('pick-chosen');
    p.hidden = false;
    p.textContent = (chosenFolderID ? 'Adding a backup for: ' : 'Protecting: ') + path;
  }

  // --- step 2: machines and their storage -------------------------------

  async function loadMachines(scan) {
    const list = $('dest-machines');
    if (scan) $('dest-scan-status').textContent = 'Looking for computers (a few seconds)…';
    const resp = await fetch('/api/machines?scan=' + (scan ? '1' : '0'));
    if (!resp.ok) { $('dest-scan-status').textContent = await resp.text(); return; }
    const machines = await resp.json();
    $('dest-scan-status').textContent = scan
      ? `Found ${machines.filter(m => m.kind === 'smb').length} other computer(s) sharing storage.`
      : '';
    list.replaceChildren();
    for (const m of machines) list.appendChild(machineRow(m));
  }

  function machineRow(m) {
    const li = mk('li', 'machine');
    const head = mk('div', 'row');
    const icon = m.kind === 'this' ? '🖥️' : (m.kind === 'paired' ? '🔗' : '🌐');
    const title = mk('button', 'link', `${icon} ${m.name}`);
    head.appendChild(title);
    if (m.addr) head.appendChild(mk('span', 'muted mono', m.addr));
    if (m.needs_auth) head.appendChild(mk('span', 'muted', '🔒 needs a password'));
    li.appendChild(head);
    if (m.note) li.appendChild(mk('p', 'muted', m.note));

    const body = mk('div', 'machine-body');
    body.hidden = true;
    li.appendChild(body);

    title.onclick = async () => {
      if (!m.browsable) {
        body.hidden = false;
        body.replaceChildren(mk('p', 'muted', m.note || 'This machine chooses its own backup folder.'));
        return;
      }
      body.hidden = !body.hidden;
      if (body.hidden || body.dataset.loaded === '1') return;
      body.replaceChildren(mk('p', 'muted', 'Looking…'));
      await loadStorage(m, body, '', '');
    };
    return li;
  }

  async function loadStorage(m, body, user, pass) {
    const resp = await mutate('/api/machines/storage', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ machine: m.id, username: user, password: pass }),
    });
    if (!resp.ok) {
      const msg = await resp.text();
      body.replaceChildren(mk('p', 'muted', msg));
      // A locked machine can be unlocked in place rather than sending the
      // user off to the command line.
      if (m.needs_auth || /password/i.test(msg)) body.appendChild(credentialsForm(m, body));
      return;
    }
    const items = await resp.json();
    body.dataset.loaded = '1';
    body.replaceChildren();
    if (!items || items.length === 0) {
      body.appendChild(mk('p', 'muted', m.kind === 'this'
        ? 'No removable drives are plugged in right now.'
        : 'No storage offered by this computer.'));
    }
    for (const s of items || []) body.appendChild(storageRow(m, s, user, pass));

    // Detected drives are a shortcut, not the whole story: a second internal
    // disk, or any folder at all, is a perfectly good destination. Without
    // this, a machine with nothing removable plugged in would offer no local
    // option whatsoever.
    if (m.kind === 'this') body.appendChild(localFolderPicker());
  }

  // A compact directory picker for choosing any folder on this computer as a
  // destination.
  function localFolderPicker() {
    const wrap = mk('details', 'local-pick');
    wrap.appendChild(mk('summary', 'muted', 'Or choose any folder on this computer'));
    const path = mk('p', 'muted mono');
    const list = mk('ul', 'picker-list');
    const up = mk('button', null, '↑ Up');
    const head = mk('div', 'row');
    head.append(up, path);

    // Browsing starts at the home folder, so a destination on another disk
    // (/mnt/backups, an external volume, anywhere outside home) would be
    // unreachable by clicking alone. Typing always works.
    const manual = mk('div', 'row');
    const box = mk('input');
    box.placeholder = '/mnt/backups';
    box.autocomplete = 'off';
    const useTyped = mk('button', null, 'Use this path');
    useTyped.onclick = () => {
      const v = box.value.trim();
      if (v) { addLocalDestination(v); box.value = ''; }
    };
    manual.append(box, useTyped);
    wrap.append(head, list, manual);

    let parent = '';
    const show = async (p) => {
      const url = p ? '/api/browse?path=' + encodeURIComponent(p) : '/api/browse';
      const resp = await fetch(url);
      if (!resp.ok) { path.textContent = await resp.text(); return; }
      const data = await resp.json();
      list.replaceChildren();
      if (data.roots) {
        parent = '';
        path.textContent = '';
        up.disabled = true;
        for (const r of data.roots) list.appendChild(pickRow(r, show));
        return;
      }
      parent = data.parent || '';
      up.disabled = !parent;
      path.textContent = data.path;
      // The folder you are looking at is itself selectable — you usually want
      // to back up *into* a folder you just navigated to.
      list.appendChild(useRow(data.path, 'Back up into this folder'));
      for (const e of data.entries || []) list.appendChild(pickRow(e, show));
    };
    up.onclick = () => show(parent);
    wrap.addEventListener('toggle', () => { if (wrap.open && !list.children.length) show(''); });
    return wrap;
  }

  function pickRow(entry, show) {
    const li = mk('li');
    const open = mk('button', 'link', '📁 ' + entry.name);
    open.onclick = () => show(entry.path);
    const use = mk('button', null, 'Use');
    use.onclick = () => addLocalDestination(entry.path);
    li.append(open, use);
    return li;
  }

  function useRow(path, label) {
    const li = mk('li');
    const use = mk('button', null, label);
    use.onclick = () => addLocalDestination(path);
    li.append(use);
    return li;
  }

  function addLocalDestination(path) {
    chosen.set(path, { path, _label: 'This computer → ' + path });
    updateCount();
    // Reflect the choice in any checkbox already rendered for the same path.
    document.querySelectorAll('.storage input[type=checkbox]').forEach(cb => {
      if (cb.dataset.key === path) cb.checked = true;
    });
  }

  function credentialsForm(m, body) {
    const wrap = mk('div', 'share-form');
    const u = mk('input'); u.placeholder = 'username';
    const p = mk('input'); p.type = 'password'; p.placeholder = 'password';
    const go = mk('button', null, 'Unlock');
    go.onclick = () => loadStorage(m, body, u.value.trim(), p.value);
    wrap.append(u, p, go);
    return wrap;
  }

  function storageRow(m, s, user, pass) {
    const key = s.path || s.url;
    const li = mk('label', 'storage');
    const cb = mk('input');
    cb.type = 'checkbox';
    cb.dataset.key = key;
    cb.checked = chosen.has(key);
    cb.onchange = () => {
      if (cb.checked) {
        chosen.set(key, s.existing_target
          ? { existing_target: s.existing_target, _label: `${m.name} → ${s.label}` }
          : {
              path: s.path || undefined,
              url: s.url || undefined,
              username: user || undefined,
              password: pass || undefined,
              _label: `${m.name} → ${s.label}`,
            });
      } else {
        chosen.delete(key);
      }
      updateCount();
    };
    li.appendChild(cb);
    li.appendChild(mk('strong', null, s.label));
    li.appendChild(mk('span', 'muted mono', key));
    if (s.total) {
      li.appendChild(mk('span', 'muted', `${fmtBytes(s.free)} free of ${fmtBytes(s.total)}`));
    }
    if (s.existing_target) {
      li.appendChild(mk('span', 'muted', `already set up as "${s.existing_target}"`));
    }
    return li;
  }

  // --- step 2b: another backup-maker machine ----------------------------

  // Syncthing device IDs are eight dash-separated groups. This is deliberately
  // the same (loose) rule setup.NormalizeDeviceID applies on the way in: a
  // paste this box accepts must never be refused by the server, or the user is
  // left arguing with two different opinions of the same ID.
  function normalizeDeviceID(raw) {
    const id = raw.trim().toUpperCase();
    if (id.length < 50 || (id.match(/-/g) || []).length < 7) return "";
    return id;
  }

  function addPairedDestination(id, name) {
    chosen.set(id, {
      device_id: id,
      name,
      _label: `${name} (another computer) → its own backup folder`,
    });
    renderPairedDestinations();
    updateCount();
  }

  // Machines added by device ID, shown as ordinary ticked destinations: once
  // added, one is no different from a drive or a share. Rebuilt from `chosen`
  // so unticking a row and removing the destination cannot disagree.
  function renderPairedDestinations() {
    const list = $("dest-paired");
    list.replaceChildren();
    for (const [key, d] of chosen) {
      if (!d.device_id) continue;
      const li = mk("li");
      const row = mk("label", "storage");
      const cb = mk("input");
      cb.type = "checkbox";
      cb.checked = true;
      cb.onchange = () => {
        chosen.delete(key);
        renderPairedDestinations();
        updateCount();
      };
      row.appendChild(cb);
      row.appendChild(mk("strong", null, d.name));
      row.appendChild(mk("span", "muted mono", key));
      row.appendChild(mk("span", "muted", "that computer chooses where backups land"));
      li.appendChild(row);
      list.appendChild(li);
    }
  }

  function updateCount() {
    const p = $('dest-count');
    p.hidden = chosen.size === 0;
    p.textContent = chosen.size === 1
      ? '1 destination chosen'
      : `${chosen.size} destinations chosen`;
  }

  function fmtBytes(n) {
    if (!n) return '?';
    if (n >= 2 ** 40) return (n / 2 ** 40).toFixed(1) + 'TB';
    if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(1) + 'GB';
    return (n / 2 ** 20).toFixed(0) + 'MB';
  }

  // --- review and commit ------------------------------------------------

  function renderReview() {
    const dl = $("wiz-review");
    dl.replaceChildren();
    const timed = mode() === "timed";
    add("Kind", timed
      ? "Timed — a full encrypted snapshot on a schedule"
      : "Incremental — a live copy, updated within seconds");
    add("Folder", chosenFolder);
    const dests = [...chosen.values()].map((d) => d._label).join("\n");
    add(chosen.size === 1 ? "Copy kept on" : `Copies kept on (${chosen.size})`, dests);
    if (timed) {
      add("Schedule", `${$("arc-every").selectedOptions[0].textContent}, keeping ${$("arc-keep").value}`);
      add("Contents", $("arc-everything").checked
        ? "everything, including node_modules and build output"
        : "your files, skipping node_modules and build output");
    }
    function add(k, v) {
      dl.appendChild(mk("dt", null, k));
      dl.appendChild(mk("dd", "mono", v));
    }
  }

  async function finish() {
    const btn = $("wiz-finish");
    btn.disabled = true;
    $("wiz-error").hidden = true;
    try {
      const dests = [...chosen.values()];
      const body = {
        folder_id: chosenFolderID || undefined,
        path: chosenFolder,
        mode: mode(),
        destinations: dests.map(({ _label, ...rest }) => rest),
      };
      // One request, one transaction: for a timed backup the schedule IS the
      // protection, so it must not be possible to save the folder without it.
      if (mode() === "timed") {
        body.archive = {
          name: $("arc-name").value.trim(),
          every: $("arc-every").value,
          keep: Number($("arc-keep").value) || 5,
          password: $("arc-pass").value,
          include_everything: $("arc-everything").checked,
        };
      }
      const resp = await mutate("/api/backups", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!resp.ok) throw new Error(await resp.text());
      const out = await resp.json();
      // A backup to another computer is not running yet: nothing copies until
      // that machine approves this one. Closing on "Start backing up" would
      // present a backup that hasn't started as a finished one.
      //
      // The names come back from the server, one per destination in the order
      // they were sent: a machine that was already a destination keeps the name
      // it already had, and the panel has to watch for THAT name.
      const saved = out.destinations || [];
      const pairedDests = dests
        .map((d, i) => (saved[i] ? { ...d, name: saved[i] } : d))
        .filter((d) => d.device_id);
      if (pairedDests.length > 0) showPairWait(pairedDests);
      else close();
      if (window.refreshDashboard) window.refreshDashboard();
      return out;
    } catch (e) {
      const p = $("wiz-error");
      p.hidden = false;
      p.textContent = e.message;
    } finally {
      btn.disabled = false;
    }
  }

  // --- waiting for the other machine to approve --------------------------

  let pairTimer = null;

  // showPairWait replaces the steps with one honest panel per paired machine.
  // The backup IS configured; it just cannot move until the other side says
  // yes, and that may be minutes (or days, if the machine is off) away.
  function showPairWait(devices) {
    document.querySelectorAll(".wiz-panel").forEach((p) => { p.hidden = true; });
    $("wiz-nav").hidden = true;
    $("wiz-error").hidden = true;
    $("wiz-progress").replaceChildren();
    $("wiz-context").textContent = "Almost there";
    $("wiz-title").textContent = "Saved — waiting to pair";
    $("wiz-lede").textContent =
      "Copying starts as soon as the other computer approves this one.";

    const list = $("pairwait-list");
    list.replaceChildren();
    const watching = [];
    for (const d of devices) {
      const li = mk("li", "card");
      li.appendChild(mk("strong", null, d.name));
      const state = mk("span", "muted");
      state.textContent =
        `Waiting for ${d.name} to approve this computer. On that machine: open its ` +
        `dashboard, or run "backup-maker pair accept". This can also mean the machine ` +
        `is off or unreachable — the pairing completes whenever it next approves.`;
      li.appendChild(state);
      list.appendChild(li);
      watching.push({ name: d.name, state });
    }
    $("wiz-pairwait").hidden = false;
    pollPairWait(watching);
  }

  // Poll rather than share the dashboard's event stream: this panel outlives a
  // single snapshot and needs an answer whether or not anything else changed.
  function pollPairWait(watching) {
    stopPairWait();
    pairTimer = setTimeout(async function loop() {
      let st = null;
      try {
        const resp = await fetch("/api/status");
        if (resp.ok) st = await resp.json();
      } catch { /* a missed poll is not worth reporting; the next one covers it */ }
      // Null means the panel was dismissed while that fetch was in flight.
      if (pairTimer === null) return;

      // Reassigned, not copied: a machine that has paired must drop out of the
      // watch list, or this would poll for ever after everything succeeded.
      watching = watching.filter((w) => {
        if (!hasPaired(st, w.name)) return true;
        w.state.className = "chosen";
        w.state.textContent =
          `${w.name} approved this computer. Your files are on their way — the ` +
          `dashboard shows the progress.`;
        return false;
      });
      if (watching.length === 0) { pairTimer = null; return; }
      pairTimer = setTimeout(loop, 3000);
    }, 3000);
  }

  // Success has to be POSITIVE evidence that copying started: a state only a
  // live sync session produces. "Not awaiting-pair" is not evidence — the
  // machine going offline, or the engine still starting, would both satisfy it
  // and turn this panel into a false all-clear.
  function hasPaired(st, name) {
    if (!st) return false;
    const t = (st.targets || []).find((x) => x.name === name);
    return !!t && (t.state === "in sync" || t.state === "syncing");
  }

  function stopPairWait() {
    if (pairTimer === null) return;
    clearTimeout(pairTimer);
    pairTimer = null;
  }

  function mk(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  // --- wiring -----------------------------------------------------------

  document.addEventListener('DOMContentLoaded', () => {
    $('wiz-next').onclick = () => {
      // The "Restore this machine" card isn't a step of this wizard — it hands
      // off to the adopt flow, which brings the user back here on Back.
      if (order[index] === 'kind' && mode() === 'adopt') {
        close();
        AdoptWizard.open(model);
        return;
      }
      const problem = canAdvance();
      if (problem) {
        const p = $('wiz-error');
        p.hidden = false;
        p.textContent = problem;
        return;
      }
      $('wiz-error').hidden = true;
      index = Math.min(order.length - 1, index + 1);
      render();
    };
    $('wiz-back').onclick = () => { index = Math.max(0, index - 1); render(); };
    // Switching kind adds or removes the schedule step.
    document.querySelectorAll('input[name="mode"]').forEach((r) => {
      r.addEventListener('change', () => { rebuildOrder(); render(); });
    });
    $('wiz-finish').onclick = finish;
    $('pick-up').onclick = (e) => {
      const parent = e.target.dataset.parent;
      if (parent) loadDir(parent); else loadRoots();
    };
    $('pick-manual-go').onclick = () => {
      const v = $('pick-manual').value.trim();
      if (v) chooseFolder(v);
    };
    $('dest-scan').onclick = () => loadMachines(true);
    $('dm-add').onclick = () => {
      const url = $('dm-url').value.trim();
      if (!url) return;
      chosen.set(url, {
        url,
        username: $('dm-user').value.trim() || undefined,
        password: $('dm-pass').value || undefined,
        _label: url,
      });
      updateCount();
      $('dm-url').value = '';
      $('dm-pass').value = '';
    };
    $('dp-add').onclick = () => {
      const err = $('dp-error');
      const id = normalizeDeviceID($('dp-id').value);
      const name = $('dp-name').value.trim();
      err.hidden = false;
      if (!id) {
        err.textContent = 'That doesn\'t look like a device ID. Run "backup-maker pair" ' +
          'on the other computer and paste the whole line it prints.';
        return;
      }
      // Named, not auto-generated: this machine has to be recognizable on the
      // dashboard months from now, and "machine-XKQ4TZ2" is not.
      if (!name) {
        err.textContent = 'Give this computer a name, so you can tell it apart later (e.g. attic-pi).';
        return;
      }
      if (chosen.has(id)) {
        err.textContent = 'That computer is already on the list.';
        return;
      }
      err.hidden = true;
      addPairedDestination(id, name);
      $('dp-id').value = '';
      $('dp-name').value = '';
    };
    $('pairwait-close').onclick = () => {
      close();
      if (window.refreshDashboard) window.refreshDashboard();
    };
    $('wiz-skip').onclick = async (e) => {
      e.preventDefault();
      await mutate('/api/setup/complete', { method: 'POST' });
      close();
    };
  });

  return { open, close, isOpen };
})();
