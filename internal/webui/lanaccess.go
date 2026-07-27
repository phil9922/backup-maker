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
	// first sight — and reports whether it is approved and the short code to
	// show it. An empty token means the browser sent none.
	Seen func(token, addr, agent string) (approved bool, code string, issued string)
}

// devicePolicy is what the wrapper needs to know about one request.
type devicePolicy struct {
	allow bool
	code  string
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
	approved, code, issued := g.Seen(token, clientAddr(r), deviceKind(r.UserAgent()))
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
	return devicePolicy{allow: approved, code: code, issued: issued}
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
// this has not been approved to know anything.
func holdingPage(w http.ResponseWriter, code string) {
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
</style></head>
<body><main>
  <h1>This device is waiting for approval</h1>
  <p>Backup status is only shown to devices approved on the computer running backup-maker.</p>
  <div class="code">` + html.EscapeString(code) + `</div>
  <p>Open the backup-maker dashboard on that computer and approve this code.
     This page checks again on its own.</p>
</main>
<script>setTimeout(function(){location.reload();}, 5000);</script>
</body></html>`))
}
