// SPDX-License-Identifier: MIT

package webui

import (
	"crypto/rand"
	"encoding/base32"
	"html"
	"net"
	"net/http"
	"strings"
)

// lanDeviceCookie carries the token the network view issues a browser. Not the
// dashboard's session cookie and deliberately a different name: these two
// listeners must never be able to borrow each other's credentials.
const lanDeviceCookie = "backupmaker_lan_device"

// LANGate decides whether one device may read the network view.
//
// WHY A TOKEN AND NOT AN ADDRESS. The obvious implementation of "only these
// devices" is an IP allow-list, and it is the wrong one: a LAN address is a
// DHCP lease that moves — so the phone that worked yesterday is refused today —
// and anyone already on the wifi can simply claim it. It would inconvenience
// the household and stop no one, which is worse than being openly open. A
// random token in a cookie is the one thing on a local network that is actually
// hard to guess.
//
// The honest cost, said plainly in the UI: approval is per BROWSER. Clearing
// cookies, or opening the view in a different browser, asks again.
type LANGate struct {
	// ApprovedOnly reports whether the gate is armed at all. When false every
	// device reads the view, exactly as before this existed.
	ApprovedOnly func() bool
	// Seen records a device arriving with this token — issuing the record on
	// first sight — and reports whether it is approved, the short code to show
	// it, and the name it has already given itself. An empty token means the
	// browser sent none.
	Seen func(token, addr, agent string) (approved bool, code, name, issued string)
	// Named records what a device calls itself, against the token it arrived
	// with. Reports whether there was a record to name — false for a browser
	// whose request has since lapsed, which is a thing to say rather than a
	// thing to silently accept.
	Named func(token, name string) bool
}

// devicePolicy is what the wrapper needs to know about one request.
type devicePolicy struct {
	allow bool
	code  string
	// name is what the device has called itself so far, so the holding page can
	// show the box already filled in rather than asking again on every reload.
	name string
	// token is the record this request belongs to, so a name posted back can be
	// filed against the device that posted it and no other.
	token string
	// issued is a token to set as a cookie, blank when the browser already had
	// a usable one.
	issued string
}

func (g *LANGate) check(w http.ResponseWriter, r *http.Request) devicePolicy {
	if g == nil || g.ApprovedOnly == nil || !g.ApprovedOnly() {
		return devicePolicy{allow: true}
	}
	var token string
	if c, err := r.Cookie(lanDeviceCookie); err == nil {
		token = c.Value
	}
	approved, code, name, issued := g.Seen(token, clientAddr(r), deviceKind(r.UserAgent()))
	if issued != "" {
		http.SetCookie(w, &http.Cookie{
			Name:  lanDeviceCookie,
			Value: issued,
			Path:  "/",
			// A year, matching the dashboard's session: a device the owner
			// approved should not have to be approved again next month.
			MaxAge:   365 * 24 * 3600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	if issued != "" {
		token = issued
	}
	return devicePolicy{allow: approved, code: code, name: name, token: token, issued: issued}
}

// maxDeviceNameBody bounds what the network listener will read from an
// unapproved device posting its name. A name is forty characters; a kilobyte is
// already absurd, and the point is that nothing about this endpoint scales with
// what the client decides to send.
const maxDeviceNameBody = 1 << 10

// handleDeviceName files the name a waiting device gave for itself.
//
// THE ONLY THING AN UNAPPROVED DEVICE MAY WRITE, and the exception is narrow on
// purpose: it writes one bounded, sanitised string, into the record its own
// cookie already identifies, about itself. It cannot name another device, it
// cannot approve itself, and nothing it writes is ever consulted to decide
// anything — the answer to this request is the same holding page either way.
func (g *LANGate) handleDeviceName(w http.ResponseWriter, r *http.Request, policy devicePolicy) {
	if g.Named == nil || policy.token == "" {
		http.Error(w, "this device is waiting for approval", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDeviceNameBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that name could not be read", http.StatusBadRequest)
		return
	}
	// A request that lapsed while somebody was typing into it does NOT land
	// here: the gate above has already filed this POST as a fresh request and
	// issued a cookie for it, so the name goes onto the live record. This is
	// the case where there is no record even after that — nothing to name, and
	// worth saying rather than answering 204 to a write that did not happen.
	if !g.Named(policy.token, r.PostFormValue("name")) {
		http.Error(w, "this request has expired — reload to ask again", http.StatusConflict)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// clientAddr is the peer address, for recognition only — never for a decision.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// deviceKind reduces a user agent to something a person can recognise in a
// list. Coarse on purpose: it is a label, not evidence.
func deviceKind(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Macintosh"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows PC"
	case strings.Contains(ua, "Linux"):
		return "Linux PC"
	default:
		return "a device"
	}
}

// NewLANDeviceToken mints a token for one browser.
func NewLANDeviceToken() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// NewLANDeviceCode is the short string shown on BOTH screens so the person
// approving can tell which device they are approving. Without it, approving
// from a list of anonymous rows is a guess.
//
// Crockford-ish alphabet: no I, O, 1 or 0, because this gets read off a phone
// and typed nowhere — it only has to be compared by eye.
func NewLANDeviceCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "??????"
	}
	out := make([]byte, 0, 7)
	for i, v := range b {
		if i == 3 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(v)%len(alphabet)])
	}
	return string(out)
}

// holdingPage is what an unapproved device sees. A whole page rather than a
// 403, because the person holding it needs to be told what to do next — and the
// code, so they can match it against the one on the dashboard.
//
// Deliberately says nothing about the backups themselves. Whoever is reading
// this has not been approved to know anything. That includes whether they were
// turned down: a denied device sees exactly this page, because "no" is a fact
// for the person at the computer, and telling the other end only invites
// another try from a different browser.
//
// IT ALSO ASKS WHO IS HOLDING IT. The dashboard end of this exchange used to
// offer a code, a coarse guess at the kind of device and an address, which
// answers "something is asking" but not "what am I letting in" — and in a house
// with three iPhones on it, an address that changes with the lease is not an
// answer. The device is the only end that knows, so it is the end that is
// asked. What comes back is a label and is treated as one: the code is still
// what gets compared before anybody presses Approve.
func holdingPage(w http.ResponseWriter, code, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Waiting for approval</title>
<style>
  :root { font-family: system-ui, sans-serif; }
  body { background:#10141a; color:#e6e9ee; margin:0; min-height:100vh;
         display:grid; place-items:center; padding:1.5rem; }
  main { max-width:22rem; text-align:center; }
  h1 { font-size:1.15rem; font-weight:600; margin:0 0 .75rem; }
  p { color:#8a94a3; line-height:1.5; margin:0 0 .75rem; }
  .code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:1.6rem;
          letter-spacing:.12em; background:#1c2430; border:1px solid #2b3542;
          border-radius:10px; padding:.6rem 1rem; display:inline-block; margin:.5rem 0 1rem; }
  form { display:flex; gap:.5rem; margin:0 0 .75rem; }
  input { flex:1 1 auto; min-width:0; font:inherit; font-size:1rem; color:#e6e9ee;
          background:#1c2430; border:1px solid #2b3542; border-radius:8px; padding:.55rem .7rem; }
  input::placeholder { color:#6b7686; }
  button { font:inherit; font-size:1rem; color:#10141a; background:#7aa2f7; border:0;
           border-radius:8px; padding:.55rem .9rem; cursor:pointer; }
  .named { color:#e6e9ee; }
</style></head>
<body><main>
  <h1>This device is waiting for approval</h1>
  <p>Backup status is only shown to devices approved on the computer running backup-maker.</p>
  <div class="code">` + html.EscapeString(code) + `</div>
  <p>Give this device a name so whoever is at that computer knows what is asking.</p>
  <form id="name-form" method="post" action="/name">
    <input id="name" name="name" value="` + html.EscapeString(name) + `" maxlength="40"
           placeholder="e.g. Alex&#39;s phone" autocomplete="off"
           aria-label="A name for this device">
    <button type="submit">Save</button>
  </form>
  <p id="said"` + hiddenUnlessNamed(name) + `>Asking as <span class="named">` +
		html.EscapeString(name) + `</span>.</p>
  <p>Open the backup-maker dashboard on that computer and approve this code.
     This page checks again on its own.</p>
</main>
<script>
// The reload is what makes approval arrive without anybody tapping anything —
// and it is also, if left alone, what wipes a half-typed name every five
// seconds. It stands down while the box is being used, which is the same guard
// the dashboard's exclude editor needs for the same reason.
(function () {
  var form = document.getElementById('name-form');
  var box = document.getElementById('name');
  var said = document.getElementById('said');
  var busy = false;
  box.addEventListener('focus', function () { busy = true; });
  box.addEventListener('blur', function () { busy = false; });
  form.addEventListener('submit', function (e) {
    e.preventDefault();
    var body = new URLSearchParams();
    body.set('name', box.value);
    fetch('/name', {method: 'POST', body: body,
                    headers: {'Content-Type': 'application/x-www-form-urlencoded'}})
      .then(function () {
        box.blur();
        busy = false;
        said.hidden = box.value.trim() === '';
        said.querySelector('.named').textContent = box.value.trim();
      })
      .catch(function () {});
  });
  setInterval(function () { if (!busy) { location.reload(); } }, 5000);
})();
</script>
</body></html>`))
}

// hiddenUnlessNamed keeps the confirmation line out of the page until there is
// something to confirm, rather than rendering an empty "Asking as ." and hiding
// it with a class somebody could forget to write a rule for.
func hiddenUnlessNamed(name string) string {
	if name == "" {
		return " hidden"
	}
	return ""
}
