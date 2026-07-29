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

  // goToStep jumps back to a named step. Used when a commit is refused for a
  // reason the user fixes further back: leaving them on the review step with an
  // error and no way to act on it is the failure this avoids.
  function goToStep(id) {
    const at = order.indexOf(id);
    if (at < 0) return;
    index = at;
    render();
  }

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
    // Opened FOR a folder — from the "Set up a backup" button on a folder that
    // nothing is copying. Carrying its id is the whole point: without it the
    // wizard opens blank, the obvious next move is to pick that same folder out
    // of the picker, and adding a path the config already holds is refused —
    // after the destination, the schedule and the password, with the id of the
    // record that already owns it. Opening blank from a button that names a
    // folder was a trap.
    if (opts && opts.folder && opts.folder.path) {
      chosenFolder = opts.folder.path;
      chosenFolderID = opts.folder.id || "";
    }
    renderChosenFolder(); // clears the line left by a previous run
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
    // No directory is open at the top level, so there is nothing for "Protect
    // this folder" to mean. Hidden rather than disabled: a greyed button here
    // invites a click that can never work.
    $('pick-here').hidden = true;
    $('pick-here').dataset.path = '';
    for (const r of data.roots || []) addPickerEntry(list, r, true);
  }

  async function loadDir(path) {
    const resp = await fetch('/api/browse?path=' + encodeURIComponent(path));
    if (!resp.ok) { alert(await resp.text()); return; }
    const data = await resp.json();
    $('pick-path').textContent = data.path;
    $('pick-up').disabled = !data.parent;
    $('pick-up').dataset.parent = data.parent || '';
    // The folder now open is itself a choice, and the obvious one: this is
    // where somebody who has navigated into what they want expects to press.
    $('pick-here').hidden = false;
    $('pick-here').dataset.path = data.path;
    $('pick-truncated').hidden = !data.truncated;
    const list = $('pick-list');
    list.replaceChildren();
    if (!data.entries || data.entries.length === 0) {
      list.appendChild(mk('li', 'muted', 'No sub-folders in here.'));
    }
    for (const e of data.entries || []) addPickerEntry(list, e, false);
  }

  // Two buttons on one row, doing different things to the same folder: the
  // name OPENS it, "Protect this" CHOOSES it. They used to sit a few pixels
  // apart with the name stretched across the whole row, so the dead space
  // between them opened the folder and a near miss on one was a hit on the
  // other. The name is now only as wide as its text, with a real gap after it
  // that does nothing at all when clicked.
  function addPickerEntry(list, entry, isRoot) {
    const li = mk('li');
    const openBtn = mk('button', 'link pick-open', (isRoot ? '' : '📁 ') + entry.name);
    openBtn.title = 'Open ' + entry.name;
    openBtn.onclick = () => loadDir(entry.path);
    const useBtn = mk('button', 'pick-use', 'Protect this');
    useBtn.title = 'Back up ' + entry.path;
    useBtn.onclick = () => chooseFolder(entry.path);
    li.append(openBtn, mk('span', 'pick-gap'), useBtn);
    list.appendChild(li);
  }

  // Offer folders already under protection, so a second kind of backup can be
  // attached to one instead of failing on the duplicate-path guard.
  //
  // SPLIT BY WHETHER ANYTHING ACTUALLY BACKS THE FOLDER UP. Being listed here
  // is not the same as being protected: a folder set up for timed snapshots
  // only is kept out of every unscoped destination, so once its last schedule
  // is deleted nothing copies it at all — while the folder record correctly
  // stays, because deleting a schedule must never delete anything else. This
  // list used to show every recorded folder under one heading, so such a folder
  // was offered as one "you're already protecting", inviting a second kind of
  // backup to be added to a first one that did not exist.
  //
  // `f.protected !== false` rather than `f.protected`: an older daemon that
  // does not send the field must keep the previous behaviour, not sweep every
  // folder into the unprotected group.
  function renderExistingFolders() {
    const folders = (model && model.folders) || [];
    const covered = folders.filter((f) => f.protected !== false);
    const bare = folders.filter((f) => f.protected === false);

    fillFolderList("existing-folders", "existing-folder-list", covered,
      "Add a backup for this");
    fillFolderList("unprotected-folders", "unprotected-folder-list", bare,
      "Set up a backup for this");
    $("existing-folders-more").hidden = folders.length === 0;
  }

  function fillFolderList(boxID, listID, folders, buttonText) {
    const box = $(boxID);
    const list = $(listID);
    box.hidden = folders.length === 0;
    list.replaceChildren();
    for (const f of folders) {
      const li = mk("li", "card");
      li.appendChild(mk("strong", null, f.label));
      li.appendChild(mk("span", "muted mono", f.path));
      const btn = mk("button", null, buttonText);
      btn.onclick = () => chooseFolder(f.path, f.id);
      li.appendChild(btn);
      list.appendChild(li);
    }
  }

  // PICKING A PATH THE CONFIG ALREADY HOLDS MEANS THAT FOLDER, not a second
  // one. Choosing it from the picker, or typing it, used to carry no id, so the
  // save was refused as a duplicate — at the very end, naming the existing
  // record by its internal id, on a page that had just said nothing was backing
  // that folder up. The two statements could not both be acted on.
  // Resolving the path to the folder that already owns it makes the refusal
  // unreachable from here: the request becomes "add a kind of backup to this
  // folder", which is what was meant.
  function chooseFolder(path, folderID) {
    chosenFolderID = folderID || folderIDForPath(path);
    chosenFolder = path;
    renderChosenFolder();
  }

  function folderIDForPath(path) {
    const want = trimSlash(path);
    for (const f of (model && model.folders) || []) {
      if (f.path && trimSlash(f.path) === want) return f.id;
    }
    return "";
  }

  function trimSlash(p) {
    return String(p || "").replace(/\/+$/, "");
  }

  // Picking a folder has to be undoable. Choosing one used to write a line of
  // text and nothing else: no way to clear it, and no sign that picking a
  // different folder would replace it — so a wrong choice looked permanent, on
  // the one step where being wrong matters most. Every other choice in this
  // wizard can be taken back; this one now can too.
  function clearChosenFolder() {
    chosenFolder = "";
    chosenFolderID = "";
    renderChosenFolder();
  }

  function renderChosenFolder() {
    const p = $('pick-chosen');
    p.replaceChildren();
    if (!chosenFolder) {
      p.hidden = true;
      return;
    }
    p.hidden = false;
    p.appendChild(mk('span', null,
      (chosenFolderID ? 'Adding a backup for: ' : 'Protecting: ') + chosenFolder));
    // "Deselect", quiet rather than red: it undoes a selection and destroys
    // nothing — nothing has even been saved yet. Red is what this dashboard
    // uses for deleting backups and erasing a drive, and it only keeps that
    // meaning if it is not also worn by un-picking a folder.
    const undo = mk('button', 'small-btn quiet', 'Deselect');
    undo.title = 'Deselect this folder and pick a different one. Nothing on disk is touched.';
    undo.onclick = clearChosenFolder;
    p.appendChild(undo);
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

    // Storage that cannot be used YET is still storage that is there. Telling
    // somebody who has just plugged a drive in that nothing is plugged in is
    // the worst answer available, and it was the one this page used to give.
    const stuck = m.kind === 'this' ? await loadUnusable() : { drives: [] };

    if ((!items || items.length === 0) && !stuck.drives.length) {
      body.appendChild(mk('p', 'muted', m.kind === 'this'
        ? 'No drives are plugged into this computer right now.'
        : 'No storage offered by this computer.'));
    }
    for (const s of items || []) body.appendChild(storageRow(m, s, user, pass));
    for (const u of stuck.drives) body.appendChild(unusableRow(u, stuck, m, body));
    if (m.kind !== 'this') body.appendChild(machineWithoutBackupMaker(m));

    // Detected drives are a shortcut, not the whole story: a second internal
    // disk, or any folder at all, is a perfectly good destination. Without
    // this, a machine with nothing removable plugged in would offer no local
    // option whatsoever.
    if (m.kind === 'this') body.appendChild(localFolderPicker());
  }

  // --- drives that are there but not usable yet -------------------------

  async function loadUnusable() {
    const empty = { drives: [], can_prepare: false, command_prefix: '', allow_command: '' };
    try {
      const resp = await fetch('/api/drives/unusable');
      if (!resp.ok) return empty;
      const data = await resp.json();
      return { ...empty, ...data, drives: data.drives || [] };
    } catch {
      return empty;
    }
  }

  // One drive that is attached but cannot hold backups, shown alongside the
  // ones that can — dimmed, unselectable, and with the reason in words.
  function unusableRow(u, ctx, m, body) {
    const wrap = mk('div', 'storage-choice unusable');
    const row = mk('div', 'storage');
    const cb = mk('input');
    cb.type = 'checkbox';
    cb.disabled = true;
    cb.title = 'This drive cannot be used yet';
    row.append(cb, mk('strong', null, u.name), mk('span', 'muted mono', u.device || u.path));
    const size = [u.size ? fmtBytes(u.size) : '', u.bus ? u.bus.toUpperCase() : ''].filter(Boolean).join(', ');
    if (size) row.appendChild(mk('span', 'muted', size));
    wrap.append(row, mk('p', 'muted small', u.detail));

    // Only a whole disk can be prepared. A directory with nothing mounted on
    // it is a different problem with a different fix, and offering to format
    // something in that state would be offering to format the system disk.
    if (u.device && u.confirm) wrap.appendChild(preparePanel(u, ctx, m, body));
    return wrap;
  }

  // The panel that erases a drive. Modelled on deleting backups: it says
  // exactly what is destroyed, it shows the command it will run, and it will
  // not act until the drive's own phrase has been typed back.
  function preparePanel(u, ctx, m, body) {
    const det = mk('details', 'prepare');
    det.appendChild(mk('summary', null, 'Set this drive up…'));

    det.appendChild(mk('p', 'warn',
      `This ERASES ${u.name} completely. Everything on it is gone, and it cannot be undone. ` +
      `Only do this to a drive whose contents you do not want — a new drive, or one you have already copied off.`));

    const mount = mk('input');
    mount.value = '/mnt/backups';
    mount.autocomplete = 'off';
    const label = mk('input');
    label.value = 'BACKUPS';
    label.autocomplete = 'off';
    det.append(
      labelled('Mount it at', mount, 'Where the drive appears on this computer. It is remembered by the drive\'s own ID, so it lands here after every reboot.'),
      labelled('Call the drive', label, 'A name for the filesystem. Cosmetic.'));

    // The typed confirmation belongs to the button. When the command has to be
    // pasted into a terminal instead, the phrase is already in it, and a box
    // that does nothing is one more thing to wonder about.
    const typed = mk('input');
    typed.autocomplete = 'off';
    typed.placeholder = u.confirm;
    if (ctx.can_prepare) {
      det.appendChild(labelled(`Type ${u.confirm} to confirm`, typed,
        'The size is the check: if it does not match the drive you meant, this is the wrong drive.'));
    }

    // The command is shown whether or not the button will work, so nothing
    // happens as root that was not visible first — and so there is something
    // to paste when the button is not available.
    const cmd = mk('pre', 'cmd');
    const refresh = () => {
      cmd.textContent = `${ctx.command_prefix || 'sudo backup-maker prepare-drive'} \\\n` +
        `  --device ${u.device} \\\n  --mount ${mount.value.trim() || '/mnt/backups'} \\\n` +
        `  --label ${label.value.trim() || 'BACKUPS'} \\\n  --confirm "${u.confirm}"`;
    };
    mount.oninput = refresh;
    label.oninput = refresh;
    refresh();

    const status = mk('p', 'muted small');
    if (ctx.can_prepare) {
      const go = mk('button', 'danger', 'Erase and set up this drive');
      go.onclick = async () => {
        if (typed.value.trim() !== u.confirm) {
          status.textContent = `Nothing was changed: type ${u.confirm} to confirm.`;
          return;
        }
        go.disabled = true;
        status.textContent = 'Setting up the drive. This takes a few seconds…';
        const resp = await mutate('/api/drives/prepare', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            device: u.device,
            mount: mount.value.trim() || '/mnt/backups',
            label: label.value.trim() || 'BACKUPS',
            confirm: typed.value.trim(),
          }),
        });
        if (!resp.ok) {
          go.disabled = false;
          status.textContent = await resp.text();
          return;
        }
        status.textContent = 'Done. The drive is ready — reloading the list…';
        body.dataset.loaded = '';
        await loadStorage(m, body, '', '');
      };
      det.append(mk('p', 'muted small', 'This will run, as an administrator:'), cmd, go, status);
    } else {
      det.append(
        mk('p', 'muted small',
          'This computer has not been set up to let the dashboard format a drive, so run this yourself in a terminal on this computer:'),
        cmd, copyButton(cmd),
        mk('p', 'muted small',
          'Or, to do it from this page instead — now and in future — run the line below once, read what it says, and reload this page:'),
        cmdBlock(ctx.allow_command));
    }
    return det;
  }

  function labelled(text, input, help) {
    const wrap = mk('div', 'field');
    const lab = mk('label', null, text);
    lab.appendChild(input);
    wrap.appendChild(lab);
    if (help) wrap.appendChild(mk('p', 'muted small', help));
    return wrap;
  }

  function cmdBlock(text) {
    const pre = mk('pre', 'cmd', text);
    const wrap = mk('div');
    wrap.append(pre, copyButton(pre));
    return wrap;
  }

  function copyButton(pre) {
    const b = mk('button', 'copy', 'Copy');
    b.onclick = async () => {
      try {
        await navigator.clipboard.writeText(pre.textContent);
        b.textContent = 'Copied';
        setTimeout(() => { b.textContent = 'Copy'; }, 1500);
      } catch {
        // Clipboard access can be refused; selecting the text still works.
        b.textContent = 'Select it and copy';
      }
    };
    return b;
  }

  // --- a computer that is not running backup-maker -----------------------

  // What this page can honestly say about a machine it can only reach as a
  // file server: it cannot see inside, and a disk plugged into that computer
  // has to be set up over there. No program can format a disk in a computer it
  // is not running on, and pretending otherwise would be the one thing worse
  // than saying so.
  function machineWithoutBackupMaker(m) {
    const wrap = mk('div', 'offbox');
    wrap.appendChild(mk('p', 'muted small',
      `backup-maker isn't running on ${m.name}, so it can only offer folders that computer already shares. ` +
      `A disk you have just plugged into it won't appear here until it has been set up over there.`));
    wrap.appendChild(mk('p', 'muted small',
      'Expecting a bigger disk than the sizes above? A shared folder can sit on the computer\'s own system disk ' +
      'rather than on the drive you meant — the size shown is the size of the disk it is really on.'));

    const det = mk('details', 'recipe');
    det.appendChild(mk('summary', 'muted', `Set up ${m.name} as a backup computer`));
    const inner = mk('div');
    det.appendChild(inner);
    det.addEventListener('toggle', async () => {
      if (!det.open || inner.dataset.loaded === '1') return;
      inner.replaceChildren(mk('p', 'muted', 'Working out the commands…'));
      const resp = await fetch('/api/machines/' + encodeURIComponent(m.id) + '/setup-recipe');
      if (!resp.ok) { inner.replaceChildren(mk('p', 'muted', await resp.text())); return; }
      inner.dataset.loaded = '1';
      inner.replaceChildren(recipeChooser(await resp.json(), m));
    });
    wrap.appendChild(det);
    return wrap;
  }

  function recipeChooser(data, m) {
    const wrap = mk('div');
    wrap.appendChild(mk('p', 'muted small',
      `Run these in a terminal on ${data.machine || m.name} — not on this computer. ` +
      `Which one depends on what kind of machine it is; this page has no way to tell, so pick it below.`));

    const pick = mk('select');
    for (const p of data.platforms || []) {
      const o = mk('option', null, p.label);
      o.value = p.id;
      pick.appendChild(o);
    }
    const pre = mk('pre', 'cmd');
    const note = mk('p', 'muted small');
    const show = () => {
      const p = (data.platforms || []).find(x => x.id === pick.value) || (data.platforms || [])[0];
      if (!p) return;
      pre.textContent = p.commands.join('\n');
      note.textContent = p.note || '';
    };
    pick.onchange = show;
    show();

    // Deliberately a button, not a timer. This program never scans the network
    // unless asked — see the comment on POST /api/scan — and a page quietly
    // sweeping the LAN every few seconds would break that promise to make a
    // spinner look busy.
    const again = mk('button', null, 'Check for it again');
    const status = mk('p', 'muted small');
    again.onclick = async () => {
      status.textContent = 'Looking…';
      await loadMachines(true);
      status.textContent = `If ${data.machine || m.name} is now running backup-maker, it appears above as a computer you can pair with.`;
    };

    wrap.append(labelled('What kind of computer is it?', pick), pre, copyButton(pre), note,
      mk('p', 'muted small',
        'Once it is running, that computer gets a dashboard of its own — open it there to set up the drive plugged into it.'),
      again, status);
    return wrap;
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

  // Same two-buttons-one-row problem as the source picker, same treatment: the
  // name is only as wide as itself and the gap between open and select is
  // inert.
  function pickRow(entry, show) {
    const li = mk('li');
    const open = mk('button', 'link pick-open', '📁 ' + entry.name);
    open.title = 'Open ' + entry.name;
    open.onclick = () => show(entry.path);
    const use = mk('button', 'pick-use', 'Use');
    use.title = 'Back up into ' + entry.path;
    use.onclick = () => addLocalDestination(entry.path);
    li.append(open, mk('span', 'pick-gap'), use);
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
      li.appendChild(mk('span', 'muted', s.existing_folder
        ? `already set up as "${s.existing_target}" (in folder ${s.existing_folder})`
        : `already set up as "${s.existing_target}"`));
    }
    // A drive can hold backups in a folder of its own rather than at its root.
    // Offered here, beside the drive it applies to, rather than only through the
    // "any folder on this computer" picker further down: that one requires the
    // directory to already exist, which is not what somebody wants when the
    // whole point is to make one.
    if (!s.path || s.existing_target) return li;
    const wrap = mk('div', 'storage-choice');
    wrap.append(li, subfolderPicker(m, s, key));
    return wrap;
  }

  // The same rules as setup.ValidateFolderName, which is authoritative. Kept in
  // step so the box can say what is wrong as it is typed; anything that slips
  // through is still refused by the server.
  function folderNameError(name) {
    if (!name) return 'Give the folder a name.';
    if (name === '.' || name === '..') return `"${name}" is not a folder name.`;
    if (/[/\\]/.test(name)) return 'A folder name cannot contain a slash — this makes one folder, not a path.';
    if (name.startsWith('.')) return 'A name starting with a dot would be hidden on most systems.';
    if (/[<>:"|?*]/.test(name)) return 'Drives and network shares refuse < > : " | ? * in a name.';
    if (name.trim() !== name) return 'A folder name cannot start or end with a space.';
    return '';
  }

  function subfolderPicker(m, s, driveKey) {
    const wrap = mk('details', 'local-pick');
    wrap.appendChild(mk('summary', 'muted', 'Or put backups in a folder on this drive'));
    const row = mk('div', 'row');
    const box = mk('input');
    box.placeholder = 'backups';
    box.autocomplete = 'off';
    const use = mk('button', null, 'Use this folder');
    const note = mk('p', 'hint');
    const preview = () => {
      const name = box.value.trim();
      const problem = folderNameError(name);
      note.textContent = !name
        ? 'The rest of the drive is left untouched.'
        : problem || `Backups will go to ${s.path.replace(/\/$/, '')}/${name} — the rest of the drive is untouched.`;
      note.classList.toggle('bad', Boolean(name && problem));
    };
    box.oninput = preview;
    use.onclick = () => {
      const name = box.value.trim();
      const problem = folderNameError(name);
      if (problem) { preview(); return; }
      const full = s.path.replace(/\/$/, '') + '/' + name;
      // Keyed on the full path, so choosing a folder and choosing the whole
      // drive are two different destinations rather than one overwriting the
      // other in the map.
      chosen.delete(driveKey);
      chosen.set(full, {
        path: full,
        create_dir: true,
        name: `${s.label}-${name}`,
        _label: `${m.name} → ${s.label} / ${name}`,
      });
      updateCount();
      note.textContent = `Chosen: ${full}`;
      note.classList.remove('bad');
    };
    row.append(box, use);
    wrap.append(row, note);
    preview();
    return wrap;
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
      // A destination whose folder for this computer belongs to another one is
      // refused with a choice, not a failure: the remedy is easy and not
      // guessable, and a paragraph of prose would leave somebody stuck.
      if (resp.status === 409) {
        showClaimConflict(await resp.json());
        return;
      }
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

  // --- another computer already uses this name ---------------------------

  // showClaimConflict turns the refusal into the two things that actually
  // resolve it. Renaming this computer is deliberately not offered: it would
  // orphan everything already backed up under the old name and copy the lot
  // again, which is not a button to put in front of somebody mid-wizard.
  function showClaimConflict(detail) {
    const p = $("wiz-error");
    p.hidden = false;
    p.replaceChildren();
    p.appendChild(mk('strong', null, detail.error));
    const row = mk('div', 'row');

    const useFolder = mk('button', null, 'Put them in a folder on this drive');
    useFolder.onclick = () => {
      // Back to the destinations step with the folder control open: this is the
      // right answer nearly every time, and it is one the user has already been
      // shown how to give.
      p.hidden = true;
      goToStep('dest');
      document.querySelectorAll('.storage-choice details').forEach((d) => { d.open = true; });
    };
    row.appendChild(useFolder);

    const takeOver = mk('button', detail.legacy ? null : 'danger',
      detail.legacy ? 'Yes, those are this computer’s' : 'This is that computer — take the name over');
    takeOver.onclick = () => {
      for (const d of chosen.values()) d.take_over = true;
      p.hidden = true;
      finish();
    };
    row.appendChild(takeOver);

    p.appendChild(row);
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
    $('pick-here').onclick = (e) => {
      const path = e.target.dataset.path;
      if (path) chooseFolder(path);
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
