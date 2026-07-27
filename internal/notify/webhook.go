// SPDX-License-Identifier: MIT

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// httpNotifyTimeout bounds one delivery over the network — a webhook POST or an
// ntfy publish. Generous enough for a phone-relay service on a slow link, short
// enough that nothing waits on it for long: the caller is a backup daemon, and
// an alert is never worth stalling a backup for.
const httpNotifyTimeout = 10 * time.Second

// Webhook posts alerts to a URL of the user's choosing, so warnings can leave
// this machine — to a phone relay, a home-automation hub, a chat room.
//
// OFF UNLESS CONFIGURED. Backing up never requires it, and nothing is sent
// anywhere until somebody sets a URL.
//
// CONTENT-FREE MODE IS NOT A GIMMICK. The last hop to a phone always crosses
// somebody else's server, and a message saying "backups to nas-attic have been
// stale for 3 days" tells that server the shape of a household. In minimal mode
// the payload says only that attention is needed, at what severity, and when —
// enough to make a phone buzz, nothing worth intercepting.
type Webhook struct {
	URL string
	// Machine is this computer's name, included only in full mode.
	Machine string
	// Minimal strips everything identifying from the payload.
	Minimal bool
	// Client is the HTTP client to use; nil means a private one with the
	// timeout above. Exists so tests can point at a recorder.
	Client *http.Client
}

// WebhookPayload is what a receiver gets. Deliberately flat and boring: it is
// consumed by whatever the user has — a relay, a shell script, a hub — and the
// simplest thing that survives contact with all of them is a small JSON object
// with stable key names.
type WebhookPayload struct {
	// Source names the program, so a receiver fed by several things can tell
	// them apart. Present in both modes: it identifies the SENDER, not the
	// household.
	Source string `json:"source"`
	// Level is "critical" (something is wrong) or "normal" (news, or an
	// all-clear). A receiver that only wants to be woken for the first has
	// everything it needs from this one field.
	Level string `json:"level"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Machine is omitted entirely in minimal mode.
	Machine string    `json:"machine,omitempty"`
	Time    time.Time `json:"time"`
}

func (w Webhook) Notify(ctx context.Context, u Urgency, title, body string) error {
	if strings.TrimSpace(w.URL) == "" {
		return fmt.Errorf("no webhook URL is configured")
	}
	level := "normal"
	if u == Critical {
		level = "critical"
	}
	p := WebhookPayload{Source: "backup-maker", Level: level, Time: time.Now()}
	if w.Minimal {
		// One fixed sentence, whatever happened. A relay can show it; nobody
		// learns anything from it.
		p.Title = "backup-maker needs attention"
	} else {
		p.Title = title
		p.Body = body
		p.Machine = w.Machine
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, httpNotifyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("the webhook URL is not usable: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backup-maker")

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: httpNotifyTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the webhook: %w", err)
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused; ignore whatever the
	// receiver said. We are not its client, only its caller.
	_, _ = resp.Body.Read(make([]byte, 512))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("the webhook answered %s", resp.Status)
	}
	return nil
}

// Sink is one delivery method, named so a failure can be attributed to it.
//
// The name matters more than it looks: without it a fan-out can only report
// THAT something failed, and "an alert could not be delivered" is not
// actionable when two methods are switched on.
type Sink struct {
	Method string
	Notifier
}

// Result is one method's outcome for one alert.
type Result struct {
	Method string
	Err    error
	At     time.Time
}

// OK reports a delivery that arrived.
func (r Result) OK() bool { return r.Err == nil }

// Multi delivers to every sink and reports the first failure, having tried
// them all.
//
// ONE SINK MUST NEVER SILENCE ANOTHER. A webhook pointing at a dead host would
// otherwise take the desktop notification down with it — the user would lose
// the alerting they had before they configured the one that broke.
type Multi []Sink

func (m Multi) Notify(ctx context.Context, u Urgency, title, body string) error {
	var first error
	for _, r := range m.Deliver(ctx, u, title, body) {
		if r.Err != nil && first == nil {
			first = r.Err
		}
	}
	return first
}

// Deliver is Notify with the per-method outcomes kept, which is what lets the
// dashboard say "the webhook last failed at 07:34" instead of staying quiet
// about alerting that has stopped working.
func (m Multi) Deliver(ctx context.Context, u Urgency, title, body string) []Result {
	out := make([]Result, 0, len(m))
	for _, s := range m {
		if s.Notifier == nil {
			continue
		}
		err := s.Notify(ctx, u, title, body)
		out = append(out, Result{Method: s.Method, Err: err, At: time.Now()})
	}
	return out
}
