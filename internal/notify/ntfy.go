// SPDX-License-Identifier: MIT

package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Ntfy publishes alerts to an ntfy topic — ntfy.sh, or a self-hosted server —
// so a phone buzzes without anything having to be run on it but the ntfy app.
//
// A SEPARATE METHOD FROM Webhook, NOT A PRESET FOR IT. A webhook posts
// backup-maker's own JSON and leaves the receiver to make sense of it; ntfy has
// a publish format of its own, and a notification that arrives with a title, a
// priority the phone actually acts on, and a tag it can draw as an icon is the
// difference between an alert somebody reads on a lock screen and a wall of
// JSON they swipe away. Both can be switched on at once.
//
// OFF UNLESS CONFIGURED. Backing up never requires it, and nothing is sent
// anywhere until somebody saves a topic.
//
// MINIMAL MODE MATTERS MORE HERE THAN ON THE WEBHOOK. An ntfy topic is not a
// secret in any strong sense — on the public server it is a name, and anyone
// who learns it can subscribe. A message saying "backups to nas-attic have been
// stale for 3 days" tells whoever is listening the shape of a household. In
// minimal mode the message says only that attention is needed, at what
// severity: enough to make the phone buzz, nothing worth intercepting.
type Ntfy struct {
	// TopicURL is the topic as the user knows it — https://ntfy.sh/my-topic,
	// or https://ntfy.example.com/my-topic for a self-hosted server.
	//
	// One field rather than a server and a topic kept apart, because one field
	// is what the ntfy app shows you and what its documentation tells you to
	// copy. Splitting it is this program's problem, not the user's.
	TopicURL string
	// Token is an ntfy access token (tk_…) for a protected topic. Optional: a
	// public topic needs none. Sent as a bearer token.
	//
	// Basic auth is reachable too, by putting the credentials in TopicURL as
	// https://user:pass@host/topic — some self-hosted instances sit behind a
	// reverse proxy that wants exactly that, and refusing to carry it would
	// send those users back to a raw webhook.
	Token string
	// Machine is this computer's name, included only in full mode.
	Machine string
	// Minimal strips everything identifying from the message.
	Minimal bool
	// Client is the HTTP client to use; nil means a private one with the
	// timeout above. Exists so tests can point at a recorder.
	Client *http.Client
	// Wait pauses for the server's Retry-After before the one retry; nil means
	// a real, cancellable sleep. Exists so tests need not actually wait.
	Wait func(ctx context.Context, d time.Duration) error
}

// ntfyMaxRetryWait bounds how long a rate-limited publish will wait for the
// server's Retry-After before giving up instead. An alert is worth a short
// pause; it is not worth holding a delivery goroutine — or the dashboard's
// Test button — open for an hour.
const ntfyMaxRetryWait = 30 * time.Second

// ntfyMessage is ntfy's JSON publish format.
//
// PUBLISHED AS JSON TO THE SERVER ROOT rather than as headers on a POST to the
// topic URL, which is the other way ntfy accepts. The header form requires
// every value to be ASCII, and the things that end up in these fields are a
// machine name, a folder label and a destination name — all of them typed by a
// user, none of them guaranteed to be ASCII. A JSON body has no such rule, so
// somebody whose laptop is called "büro" gets a working alert instead of a
// mangled one.
type ntfyMessage struct {
	Topic   string `json:"topic"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
	// Priority is ntfy's 1–5 scale. 5 is what breaks through a phone's own
	// quiet hours if the user has told it to; 3 is ordinary.
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

func (n Ntfy) Notify(ctx context.Context, u Urgency, title, body string) error {
	base, topic, err := n.endpoint()
	if err != nil {
		return err
	}
	msg := ntfyMessage{Topic: topic, Priority: 3, Tags: []string{"floppy_disk"}}
	if u == Critical {
		// max priority and a tag the phone draws as a warning triangle. A
		// stopped backup is the one thing this program exists to interrupt
		// somebody about.
		msg.Priority = 5
		msg.Tags = []string{"warning"}
	}
	if n.Minimal {
		// One fixed pair of sentences, whatever happened. The severity still
		// travels — that is carried by Priority, which is a number, not a
		// description of anyone's house.
		msg.Title = "backup-maker needs attention"
		msg.Message = "Open the dashboard on that computer to see what."
	} else {
		// The machine name goes in the TITLE, not the body: on a lock screen
		// the title is the line that survives truncation, and "my-laptop: card
		// is stale" is the whole alert for somebody who runs more than one
		// machine.
		msg.Title = title
		if n.Machine != "" {
			msg.Title = n.Machine + ": " + title
		}
		msg.Message = body
		if strings.TrimSpace(msg.Message) == "" {
			// ntfy substitutes its own placeholder for an empty message, which
			// would read as a fault in backup-maker. Say the thing again rather
			// than let the server invent something.
			msg.Message = title
		}
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	status, retryAfter, err := n.post(ctx, base, raw)
	if err != nil {
		return err
	}
	if status == http.StatusTooManyRequests && retryAfter > 0 && retryAfter <= ntfyMaxRetryWait {
		// ONE RETRY, ONLY WHEN THE SERVER SAID HOW LONG. ntfy.sh limits
		// anonymous publishing per source address, and on a laptop whose
		// traffic leaves through a VPN that address is shared with strangers,
		// so a first 429 is often somebody else's doing and clears in seconds.
		// Waiting once, for exactly what it asked, is the difference between
		// an alert that arrives late and one that never arrives.
		if err := n.wait(ctx, retryAfter); err != nil {
			return fmt.Errorf("the ntfy server is rate-limiting this address; gave up waiting to retry: %w", err)
		}
		status, retryAfter, err = n.post(ctx, base, raw)
		if err != nil {
			return err
		}
	}
	return ntfyStatusError(status, retryAfter)
}

// post publishes one message and reports the status and any Retry-After.
func (n Ntfy) post(ctx context.Context, base *url.URL, raw []byte) (int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, httpNotifyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(raw))
	if err != nil {
		return 0, 0, fmt.Errorf("the ntfy address is not usable: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "backup-maker")
	// The token wins if both are present: it is the field somebody filled in on
	// purpose, whereas credentials in the address may be left over from a proxy
	// that no longer needs them. Only one Authorization header exists, so the
	// choice has to be made here rather than by whichever line runs last.
	if t := strings.TrimSpace(n.Token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	} else if base.User != nil {
		pass, _ := base.User.Password()
		req.SetBasicAuth(base.User.Username(), pass)
	}

	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: httpNotifyTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("could not reach the ntfy server: %w", err)
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused; ignore what it said.
	_, _ = resp.Body.Read(make([]byte, 512))
	return resp.StatusCode, retryAfter(resp.Header.Get("Retry-After")), nil
}

// ntfyStatusError turns a final status into the sentence the dashboard shows.
func ntfyStatusError(status int, retryAfter time.Duration) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// Named rather than left as a bare status, because it is the commonest
		// way this method is misconfigured and the fix is a specific one.
		return fmt.Errorf("the ntfy server refused this topic (%d %s) — a protected topic needs an access token",
			status, http.StatusText(status))
	case status == http.StatusTooManyRequests:
		// Also named, because the bare status sent somebody looking at their
		// own settings for a fault that was in the shared address they were
		// publishing from. The two remedies are the two that actually work.
		msg := "the ntfy server is rate-limiting this address (429 Too Many Requests)"
		if retryAfter > ntfyMaxRetryWait {
			msg += fmt.Sprintf(", and asked for a %s wait", retryAfter.Round(time.Second))
		}
		return fmt.Errorf("%s — usual when this computer's traffic leaves through a VPN, whose exit address is shared; "+
			"take backup-maker out of the VPN, or use an ntfy access token so the limit applies to your account instead", msg)
	default:
		return fmt.Errorf("the ntfy server answered %d %s", status, http.StatusText(status))
	}
}

func (n Ntfy) wait(ctx context.Context, d time.Duration) error {
	if n.Wait != nil {
		return n.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads a Retry-After header, in either of its two forms. 0 means
// absent or unreadable — never a guess, since the caller waits on it.
func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(h); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// endpoint splits the topic URL into the server to post to and the topic to
// name in the body.
//
// The last path segment is the topic and everything before it is the server, so
// a self-hosted instance mounted under a path — https://example.com/ntfy/alerts
// — resolves to the right root instead of the domain's.
func (n Ntfy) endpoint() (*url.URL, string, error) {
	raw := strings.TrimSpace(n.TopicURL)
	if raw == "" {
		return nil, "", fmt.Errorf("no ntfy topic has been set")
	}
	if !strings.Contains(raw, "://") {
		// Somebody who pasted "ntfy.sh/my-topic" meant https. Assumed rather
		// than guessed both ways: this carries a token, and the assumption is
		// only ever made in the safe direction.
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "", fmt.Errorf("the ntfy address is not usable: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("an ntfy address has to be http or https, not %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, "", fmt.Errorf("that is a topic name, not an address — use the whole thing, like https://ntfy.sh/your-topic")
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		// NOT DEFAULTED TO ANYTHING. Inventing a topic would publish a
		// household's alerts to a name somebody else may already be reading.
		return nil, "", fmt.Errorf("that address has no topic on the end of it — use something like https://ntfy.sh/your-topic")
	}
	cut := strings.LastIndex(path, "/")
	topic := path[cut+1:]
	base := *u
	base.Path = "/" + path[:cut+1]
	base.RawQuery = ""
	base.Fragment = ""
	return &base, topic, nil
}
