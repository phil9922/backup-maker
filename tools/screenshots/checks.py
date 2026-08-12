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

# Text that cannot be read against the surface behind it.
#
# THIS USED TO TEST ONLY FOR AN EXACT MATCH, and an exact match is the easy half.
# #wiz-finish — the button that starts the backup — was mid-green on mid-blue for
# releases: an id rule set the text colour green back when the button had no
# fill, the button was later given class="primary" which fills it accent blue,
# and green is not equal to blue, so this guard waved it through. It shipped in
# the README's own tour of the wizard and was found by a person squinting at it.
#
# Contrast ratio, then, not equality — WCAG's formula, 3:1, which is the large-
# text threshold. Deliberately lenient: the point is to catch labels nobody can
# read, not to hold this dashboard to AA on every muted hint.
INVISIBLE_TEXT = """() => {
  const lum = (c) => {
    const [r, g, b] = c.match(/[\\d.]+/g).slice(0, 3).map(Number).map((v) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const ratio = (a, b) => {
    const x = lum(a), y = lum(b);
    return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
  };
  // The nearest ancestor that actually paints something, since a transparent
  // background means the text sits on whatever is behind its parent.
  const behind = (e) => {
    for (let n = e; n; n = n.parentElement) {
      const bg = getComputedStyle(n).backgroundColor;
      if (bg && bg !== 'rgba(0, 0, 0, 0)' && !bg.startsWith('rgba(0, 0, 0, 0')) return bg;
    }
    return 'rgb(11, 15, 20)';
  };
  const bad = [];
  for (const e of document.querySelectorAll('button, a, input, label, td, th, span, p, h1, h2, h3')) {
    if (!e.offsetParent) continue;
    const own = [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
    if (!own) continue; // a wrapper is judged by its own words, not its children's
    const s = getComputedStyle(e);
    const bg = behind(e);
    const r = ratio(s.color, bg);
    if (r < 3) {
      bad.push((e.id ? '#' + e.id : e.tagName) +
               ' "' + e.textContent.trim().slice(0, 40) + '" ' +
               s.color + ' on ' + bg + ' = ' + r.toFixed(2) + ':1');
    }
  }
  return bad;
}"""


def no_invisible_text(page, out):
    found = page.evaluate(INVISIBLE_TEXT)
    if found:
        raise SystemExit(
            f"{out}: text cannot be read against what is behind it: {found}. "
            "Refusing to publish a shot with an unreadable label — this is the "
            "failure mode that shipped three times already, most recently the "
            "wizard's own \"Start backing up\" button in green on blue.")


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
