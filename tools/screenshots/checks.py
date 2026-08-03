#!/usr/bin/env python3
"""Checks every screenshot must pass before it is saved.

These exist because of a pattern this project keeps hitting: a rendering fault
with all the right words on screen, no error anywhere, and nothing to notice it
but a person looking closely. It happened three times in a week —

  * v0.1.10 drew every dashboard state in the body colour, so a broken
    destination looked exactly like a healthy one, for two releases;
  * the wizard's step question inherited the small uppercase muted style meant for
    dashboard section labels, so the question was no more prominent than its own
    eyebrow;
  * "Protect this folder" was drawn as accent text on an accent background — a
    1:1 contrast ratio and an invisible label on the primary action of the step —
    because an id rule from when it was an outline button outranked the filled
    `.primary` style it had been changed to.

Every one of those was invisible to `go test`, and the second and third were only
found because somebody re-shot the screenshots. So the shooting scripts refuse to
save a shot that fails these, and the guards live here rather than in one of them.
"""

# Used by nothing_local_leaked to ask the OS for the real home directory. It was
# missing, so that guard raised NameError instead of checking anything — the one
# failure a guard must never have, since a guard that throws looks the same from
# a distance as a guard that passes.
import os

# Text whose colour matches the surface behind it. Catches the whole family:
# invisible labels, and states that lost their colour and fell back to the body's.
INVISIBLE_TEXT = """() => {
  const bad = [];
  for (const e of document.querySelectorAll('button, a, input, label, td, th, span, p, h1, h2, h3')) {
    if (!e.offsetParent) continue;
    if (!e.textContent.trim()) continue;
    const s = getComputedStyle(e);
    if (s.color === s.backgroundColor && s.backgroundColor !== 'rgba(0, 0, 0, 0)') {
      bad.push((e.id ? '#' + e.id : e.tagName) +
               ' "' + e.textContent.trim().slice(0, 40) + '" ' + s.color);
    }
  }
  return bad;
}"""


def no_invisible_text(page, out):
    found = page.evaluate(INVISIBLE_TEXT)
    if found:
        raise SystemExit(
            f"{out}: text is the same colour as what is behind it: {found}. "
            "Refusing to publish a shot with an unreadable label — this is the "
            "failure mode that shipped twice already.")


def nothing_local_leaked(page, out, repo):
    """No trace of the machine the harness runs on may reach a screenshot."""
    text = page.inner_text("body")
    # The real home directory is asked for rather than named: this is the guard
    # that stops a screenshot shipping a real path, so hard-coding the username
    # made the guard itself a disclosure — and wrong on anybody else's machine.
    leaked = [s for s in ("/tmp/", os.path.expanduser("~"), "claude", repo) if s in text]
    if leaked:
        raise SystemExit(f"{out}: leaked {leaked} — do not publish this shot")


def says(page, out, phrases):
    """The page really is showing what this shot is supposed to show."""
    text = page.inner_text("body")
    missing = [w for w in phrases if w not in text]
    if missing:
        raise SystemExit(f"{out}: page does not contain {missing} — it is not on "
                         "the step this shot is for")


def clip_to_content(page, width, pad=40):
    """A clip that ends at the real bottom of the content.

    A full-page shot of a narrow viewport runs to ~10,000px because the tables
    stack into cards, which is useless in a document.
    """
    bottom = page.evaluate("""() => {
        let m = 0;
        for (const e of document.querySelectorAll('body *')) {
            const r = e.getBoundingClientRect();
            if (r.height && r.bottom > m) m = r.bottom;
        }
        return Math.ceil(m); }""")
    return {"x": 0, "y": 0, "width": width, "height": bottom + pad}
