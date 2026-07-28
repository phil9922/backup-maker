// SPDX-License-Identifier: MIT

// The "Restore this machine" flow: rebuild a fresh install's configuration from
// backups that already exist on a drive or a network share. It reads a
// destination's manifest, lets the user decide whether to continue as the old
// machine or start fresh, and re-points every folder, share and snapshot.
//
// It shares the setup wizard's look and CSS but not its DOM — the two flows are
// separate #main panels so neither can leave the other half-rendered.
const AdoptWizard = (() => {
  const $ = (id) => document.getElementById(id);

  const STEPS = {
    source: {
      chip: "Backups",
      title: "Where are your backups?",
      lede: "Pick an attached drive, browse this computer, or connect to the share they live on.",
    },
    identity: {
      chip: "Identity",
      title: "Is this the same machine?",
      lede: "Continue where the old machine left off, or start a fresh copy alongside it.",
    },
    folders: {
      chip: "Folders",
      title: "Where do these folders live now?",
      lede: "Folders found in place are ready. Point the rest at their new location — or leave them for later.",
    },
    passwords: {
      chip: "Passwords",
      title: "Unlock shares and snapshots",
      lede: "Anything you skip stays paused until you enter its password later.",
    },
    review: {
      chip: "Review",
      title: "Ready to restore",
      lede: "This rebuilds your configuration from what the backups already know.",
    },
  };

  let order = [];
  let index = 0;
  let model = null;          // last status model, for handing back to the wizard
  let inspection = null;     // the manifest a destination revealed
  let source = null;         // the AdoptSourceRequest that produced it
  let continueAsMachine = true;
  let newMachineName = "";
  const pathRemap = new Map();       // folder ID -> new absolute path
  const sharePasswords = new Map();  // share target name -> tested-OK password
  const archivePasswords = new Map();// archive name -> password

  function isOpen() { return !$("adopt").hidden; }

  function open(st) {
    model = st || null;
    inspection = null;
    source = null;
    continueAsMachine = true;
    newMachineName = "";
    pathRemap.clear();
    sharePasswords.clear();
    archivePasswords.clear();
    order = ["source", "identity", "folders", "review"];
    index = 0;
    $("adopt-error").hidden = true;
    $("adopt-candidates").replaceChildren();
    $("adopt-browse").replaceChildren();
    $("adopt-browse-wrap").open = false;
    $("adopt-share-wrap").open = false;
    $("adopt-share-url").value = "";
    $("adopt-share-user").value = "";
    $("adopt-share-pass").value = "";
    $("adopt-identity").replaceChildren();
    $("adopt-folders").replaceChildren();
    $("adopt-passwords").replaceChildren();
    $("wizard").hidden = true;
    $("dashboard").hidden = true;
    $("adopt").hidden = false;
    scan();
    render();
  }

  function close() {
    $("adopt").hidden = true;
    $("dashboard").hidden = false;
  }

  // A configured machine can't adopt: 409 means this machine moved on while the
  // flow was open. Bail out to the dashboard rather than pretending to continue.
  function conflict() {
    close();
    if (window.refreshDashboard) window.refreshDashboard();
  }

  function render() {
    const id = order[index];
    const step = STEPS[id];

    document.querySelectorAll("#adopt .wiz-panel").forEach((p) => {
      p.hidden = p.dataset.step !== id;
    });
    $("adopt-title").textContent = step.title;
    $("adopt-lede").textContent = step.lede;
    $("adopt-context").textContent = `Restore this machine · step ${index + 1} of ${order.length}`;

    const prog = $("adopt-progress");
    prog.replaceChildren();
    order.forEach((sid, i) => {
      prog.appendChild(mk("li", i === index ? "current" : (i < index ? "done" : ""), STEPS[sid].chip));
    });

    // Back on the first step returns to the setup wizard rather than dead-ending.
    $("adopt-back").disabled = false;
    const last = index === order.length - 1;
    $("adopt-next").hidden = last;
    $("adopt-finish").hidden = !last;
    if (id === "folders") updateFoldersNote();
    if (id === "review") renderReview();
  }

  function canAdvance() {
    if (order[index] === "source" && !inspection) {
      return "Choose a backup to restore from first.";
    }
    return "";
  }

  // The password step only exists when there is something to unlock: a share
  // whose credentials aren't already in hand, or an encrypted snapshot.
  function hasPasswordStep() {
    if (!inspection) return false;
    const shares = (inspection.targets || []).filter((t) => t.type === "share" && !t.pointed_at);
    return shares.length > 0 || (inspection.archives || []).length > 0;
  }

  function rebuildOrder() {
    order = hasPasswordStep()
      ? ["source", "identity", "folders", "passwords", "review"]
      : ["source", "identity", "folders", "review"];
  }

  // --- step 1: source ---------------------------------------------------

  async function scan() {
    const status = $("adopt-scan-status");
    status.textContent = "Looking for attached backup drives…";
    try {
      const resp = await fetch("/api/adopt/scan");
      if (resp.status === 409) return conflict();
      if (!resp.ok) throw new Error(await resp.text());
      const data = await resp.json();
      const list = data.candidates || [];
      renderCandidates(list);
      status.textContent = list.length === 0
        ? "No backup drives detected. Browse this computer or connect to a share below."
        : "";
    } catch (e) {
      status.textContent = e.message;
    }
  }

  function renderCandidates(list) {
    const box = $("adopt-candidates");
    box.replaceChildren();
    for (const c of list) {
      const li = mk("li", "card");
      li.appendChild(mk("strong", null, `Backups from “${c.machine_name}”`));
      li.appendChild(mk("span", "muted", `${c.folders} folder(s) · updated ${humanTime(c.generated)}`));
      li.appendChild(mk("span", "muted mono", c.path));
      const btn = mk("button", null, "Restore from this");
      // The machine is named explicitly: one drive can hold several, and the
      // row the user clicked says whose backups they mean. Without it the
      // server would fall back to the most recent, which on a shared drive is
      // somebody else's configuration restored onto this computer.
      btn.onclick = () => inspect({ path: c.path, machine: c.machine_name });
      li.appendChild(btn);
      box.appendChild(li);
    }
  }

  // Reading a manifest is the gate into every later step: it decides the machine
  // name, the folders, the shares and the snapshots. A failure (no manifest, bad
  // credentials) stays on this step with an inline message.
  async function inspect(src) {
    const err = $("adopt-error");
    err.hidden = true;
    try {
      const resp = await mutate("/api/adopt/inspect", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(src),
      });
      if (resp.status === 409) return conflict();
      if (!resp.ok) throw new Error(await resp.text());
      inspection = await resp.json();
      source = src;
      // A different source means different decisions: start them clean.
      continueAsMachine = true;
      newMachineName = "";
      pathRemap.clear();
      sharePasswords.clear();
      archivePasswords.clear();
      buildIdentity();
      buildFolders();
      buildPasswords();
      rebuildOrder();
      index = 1;
      render();
    } catch (e) {
      err.hidden = false;
      err.textContent = e.message;
    }
  }

  // --- step 2: identity -------------------------------------------------

  function buildIdentity() {
    const box = $("adopt-identity");
    box.replaceChildren();
    buildMachineChoice(box);
    const ul = mk("ul", "choices");

    const cont = mk("input");
    cont.type = "radio";
    cont.name = "adopt-identity";
    cont.checked = true;
    ul.appendChild(choiceCard(cont,
      `Continue as “${inspection.machine_name}”`,
      "Backups keep building exactly where they are. Best for a reinstall or a replacement machine."));

    const fresh = mk("input");
    fresh.type = "radio";
    fresh.name = "adopt-identity";
    const freshCard = choiceCard(fresh, "Start fresh as:",
      "Keeps the old machine's backups untouched; this machine builds its own copy alongside.");
    const name = mk("input");
    name.placeholder = "(this computer's name)";
    name.autocomplete = "off";
    // Typing a name is itself a choice to start fresh.
    name.onfocus = () => { fresh.checked = true; continueAsMachine = false; };
    name.oninput = () => { newMachineName = name.value.trim(); };
    freshCard.querySelector(".choice-body").appendChild(name);
    ul.appendChild(freshCard);

    cont.onchange = fresh.onchange = () => { continueAsMachine = cont.checked; };
    box.appendChild(ul);
  }

  // A destination can hold backups from more than one computer. Which one is
  // being restored decides everything after this, so it is said out loud — and
  // switching re-inspects rather than reinterpreting what is already loaded,
  // because every later step was built from the manifest we picked.
  function buildMachineChoice(box) {
    const machines = inspection.machines || [];
    if (machines.length < 2) return;
    const wrap = mk("div", "notice");
    wrap.appendChild(mk("strong", null,
      `This storage holds backups from ${machines.length} computers.`));
    wrap.appendChild(mk("p", "hint",
      `Restoring “${inspection.machine_name}”. Choose a different one if that is not this computer.`));
    const row = mk("div", "row");
    for (const m of machines) {
      if (m.machine_name === inspection.machine_name) continue;
      const btn = mk("button", null, `Restore “${m.machine_name}” instead`);
      btn.onclick = () => inspect(Object.assign({}, source, { machine: m.machine_name }));
      row.appendChild(btn);
    }
    wrap.appendChild(row);
    box.appendChild(wrap);
  }

  function choiceCard(input, title, copy) {
    const li = mk("li");
    const label = mk("label", "choice");
    const body = mk("span", "choice-body");
    body.appendChild(mk("strong", null, title));
    body.appendChild(mk("span", "muted", copy));
    label.append(input, body);
    li.appendChild(label);
    return li;
  }

  // --- step 3: folders --------------------------------------------------

  function buildFolders() {
    const box = $("adopt-folders");
    box.replaceChildren();
    for (const f of inspection.folders || []) {
      const li = mk("li", "card");
      if (f.exists) {
        li.appendChild(mk("strong", null, "✓ " + f.label));
        li.appendChild(mk("span", "muted mono", f.path));
        box.appendChild(li);
        continue;
      }
      li.appendChild(mk("strong", null, "⚠ " + f.label));
      li.appendChild(mk("span", "muted mono", f.path));
      li.appendChild(mk("span", "muted", "not found here"));
      const chosen = mk("span", "chosen");
      chosen.hidden = true;
      const picker = mk("div");
      picker.hidden = true;
      const pick = mk("button", null, "Pick new location…");
      pick.onclick = () => {
        if (picker.children.length === 0) {
          picker.appendChild(folderPicker((p) => {
            pathRemap.set(f.id, p);
            chosen.hidden = false;
            chosen.textContent = "New location: " + p;
            picker.hidden = true;
            updateFoldersNote();
          }));
        }
        picker.hidden = !picker.hidden;
      };
      li.append(pick, chosen, picker);
      box.appendChild(li);
    }
  }

  function updateFoldersNote() {
    const note = $("adopt-folders-note");
    const waiting = (inspection ? inspection.folders || [] : [])
      .filter((f) => !f.exists && !pathRemap.get(f.id)).length;
    note.hidden = waiting === 0;
    note.textContent = waiting === 1
      ? "1 folder will wait until its location is set."
      : `${waiting} folders will wait until their locations are set.`;
  }

  // --- step 4: passwords ------------------------------------------------

  function buildPasswords() {
    const box = $("adopt-passwords");
    box.replaceChildren();
    for (const t of (inspection.targets || []).filter((t) => t.type === "share")) {
      box.appendChild(t.pointed_at ? pointedShareRow(t) : sharePasswordRow(t));
    }
    for (const a of inspection.archives || []) {
      box.appendChild(archivePasswordRow(a));
    }
  }

  // The share we inspected: its password is already in hand server-side, so it
  // is shown only for reassurance.
  function pointedShareRow(t) {
    const li = mk("li", "card");
    li.appendChild(mk("strong", null, t.name));
    li.appendChild(mk("span", "muted mono", t.location));
    li.appendChild(mk("span", "muted", "using the credentials you just entered"));
    return li;
  }

  function sharePasswordRow(t) {
    const li = mk("li", "card");
    li.appendChild(mk("strong", null, t.name));
    li.appendChild(mk("span", "muted mono", t.location));
    if (t.username) li.appendChild(mk("span", "muted", "user: " + t.username));

    const form = mk("div", "share-form");
    const pw = mk("input");
    pw.type = "password";
    pw.placeholder = "password";
    pw.autocomplete = "new-password";
    const test = mk("button", null, "Test");
    const status = mk("span", "muted");
    // Only a password proven to connect is stored — an untested one is omitted,
    // leaving that share paused rather than silently misconfigured.
    test.onclick = async () => {
      status.className = "muted";
      status.textContent = "Testing…";
      try {
        const resp = await mutate("/api/adopt/test-share", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ url: t.location, username: t.username || "", password: pw.value }),
        });
        if (resp.status === 409) return conflict();
        if (!resp.ok) throw new Error(await resp.text());
        sharePasswords.set(t.name, pw.value);
        status.className = "field-ok";
        status.textContent = "✓ connected";
      } catch (e) {
        sharePasswords.delete(t.name);
        status.className = "field-error";
        status.textContent = "✗ " + e.message;
      }
    };
    // Editing after a successful test invalidates it.
    pw.oninput = () => {
      if (sharePasswords.has(t.name)) {
        sharePasswords.delete(t.name);
        status.className = "muted";
        status.textContent = "";
      }
    };
    form.append(pw, test, status);
    li.appendChild(form);
    li.appendChild(mk("span", "muted small", "Skip to leave it paused until you enter its password later."));
    return li;
  }

  function archivePasswordRow(a) {
    const li = mk("li", "card");
    li.appendChild(mk("strong", null, a.name));
    li.appendChild(mk("span", "muted", `snapshot, ${a.every} → ${a.target}`));
    const form = mk("div", "share-form");
    const pw = mk("input");
    pw.type = "password";
    pw.placeholder = "password";
    pw.autocomplete = "new-password";
    // No connection to test against — the password is simply stored (or not).
    pw.oninput = () => {
      if (pw.value) archivePasswords.set(a.name, pw.value);
      else archivePasswords.delete(a.name);
    };
    form.appendChild(pw);
    li.appendChild(form);
    li.appendChild(mk("span", "muted small", "Skip to set it later on the dashboard; the snapshot waits until then."));
    return li;
  }

  // --- shared folder picker --------------------------------------------

  // A self-contained directory picker: navigate, or type a path, and every
  // choice is handed to onPick. Used for the source browse and for remapping a
  // folder that isn't where the manifest expected it.
  function folderPicker(onPick) {
    const wrap = mk("div", "picker");
    const head = mk("div", "row");
    const up = mk("button", null, "↑ Up");
    const path = mk("span", "muted mono");
    head.append(up, path);
    const list = mk("ul", "picker-list");
    const manual = mk("div", "row");
    const box = mk("input");
    box.placeholder = "/mnt/backups";
    box.autocomplete = "off";
    const useTyped = mk("button", null, "Use this path");
    useTyped.onclick = () => { const v = box.value.trim(); if (v) onPick(v); };
    manual.append(box, useTyped);
    wrap.append(head, list, manual);

    let parent = "";
    const show = async (p) => {
      const url = p ? "/api/browse?path=" + encodeURIComponent(p) : "/api/browse";
      const resp = await fetch(url);
      if (!resp.ok) { path.textContent = await resp.text(); return; }
      const data = await resp.json();
      list.replaceChildren();
      if (data.roots) {
        parent = "";
        path.textContent = "";
        up.disabled = true;
        for (const r of data.roots) list.appendChild(pickRow(r, show, onPick));
        return;
      }
      parent = data.parent || "";
      up.disabled = !parent;
      path.textContent = data.path;
      // The folder you navigated to is itself selectable.
      list.appendChild(useRow(data.path, "Use this folder", onPick));
      for (const e of data.entries || []) list.appendChild(pickRow(e, show, onPick));
    };
    up.onclick = () => show(parent);
    show("");
    return wrap;
  }

  // The name opens the folder and "Use" chooses it; they must not sit a few
  // pixels apart with stretched dead space between them that opens on click.
  // See the same treatment in wizard.js.
  function pickRow(entry, show, onPick) {
    const li = mk("li");
    const open = mk("button", "link pick-open", "📁 " + entry.name);
    open.title = "Open " + entry.name;
    open.onclick = () => show(entry.path);
    const use = mk("button", "pick-use", "Use");
    use.title = "Use " + entry.path;
    use.onclick = () => onPick(entry.path);
    li.append(open, mk("span", "pick-gap"), use);
    return li;
  }

  function useRow(path, label, onPick) {
    const li = mk("li");
    const use = mk("button", null, label);
    use.onclick = () => onPick(path);
    li.append(use);
    return li;
  }

  // --- review and commit ------------------------------------------------

  function renderReview() {
    const dl = $("adopt-review");
    dl.replaceChildren();
    const add = (k, v) => {
      dl.appendChild(mk("dt", null, k));
      dl.appendChild(mk("dd", "mono", v));
    };

    add("This machine", continueAsMachine
      ? `Continuing as ${inspection.machine_name}`
      : `Starting fresh as ${newMachineName || "(this computer's name)"}`);

    const folders = inspection.folders || [];
    const waiting = folders.filter((f) => !f.exists && !pathRemap.get(f.id)).length;
    add("Folders", `${folders.length}${waiting ? ` (${waiting} waiting for a location)` : ""}`);

    const targets = inspection.targets || [];
    const needPw = targets.filter((t) => t.type === "share" && !t.pointed_at && !sharePasswords.has(t.name)).length;
    add("Destinations", `${targets.length}${needPw ? ` (${needPw} without a password yet)` : ""}`);

    const archives = inspection.archives || [];
    if (archives.length) {
      const noPw = archives.filter((a) => !archivePasswords.has(a.name)).length;
      add("Snapshots", `${archives.length}${noPw ? ` (${noPw} without a password)` : ""}`);
    }
  }

  async function finish() {
    const btn = $("adopt-finish");
    btn.disabled = true;
    $("adopt-error").hidden = true;
    try {
      const body = {
        source,
        continue_as_machine: continueAsMachine,
        new_machine_name: continueAsMachine ? "" : newMachineName,
        path_remap: mapToObj(pathRemap),
        share_passwords: mapToObj(sharePasswords),
        archive_passwords: mapToObj(archivePasswords),
      };
      const resp = await mutate("/api/adopt", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (resp.status === 409) return conflict();
      if (!resp.ok) throw new Error(await resp.text());
      const out = await resp.json();
      close();
      if (window.refreshDashboard) window.refreshDashboard();
      return out;
    } catch (e) {
      const p = $("adopt-error");
      p.hidden = false;
      p.textContent = e.message;
    } finally {
      btn.disabled = false;
    }
  }

  function mapToObj(m) {
    const o = {};
    for (const [k, v] of m) o[k] = v;
    return o;
  }

  function mk(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  // --- wiring -----------------------------------------------------------

  document.addEventListener("DOMContentLoaded", () => {
    $("adopt-next").onclick = () => {
      const problem = canAdvance();
      if (problem) {
        const p = $("adopt-error");
        p.hidden = false;
        p.textContent = problem;
        return;
      }
      $("adopt-error").hidden = true;
      index = Math.min(order.length - 1, index + 1);
      render();
    };
    $("adopt-back").onclick = () => {
      $("adopt-error").hidden = true;
      if (index === 0) { close(); Wizard.open(model, { firstRun: true }); return; }
      index = Math.max(0, index - 1);
      render();
    };
    $("adopt-finish").onclick = finish;
    $("adopt-share-go").onclick = () => {
      const url = $("adopt-share-url").value.trim();
      if (!url) return;
      inspect({ url, username: $("adopt-share-user").value.trim(), password: $("adopt-share-pass").value });
    };
    // Build the browse picker lazily, the first time its disclosure opens.
    $("adopt-browse-wrap").addEventListener("toggle", () => {
      const wrap = $("adopt-browse-wrap");
      const box = $("adopt-browse");
      if (wrap.open && box.children.length === 0) {
        box.appendChild(folderPicker((p) => inspect({ path: p })));
      }
    });
  });

  return { open, close, isOpen };
})();
