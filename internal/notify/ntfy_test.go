// SPDX-License-Identifier: MIT

package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ntfyRecord is what a fake ntfy server saw: the path posted to, the decoded
// body, and the Authorization header.
type ntfyRecord struct {
	Path string
	Msg  ntfyMessage
	Auth string
}

func fakeNtfy(t *testing.T, status int) (*Ntfy, *[]ntfyRecord) {
	t.Helper()
	var got []ntfyRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m ntfyMessage
		_ = json.Unmarshal(body, &m)
		got = append(got, ntfyRecord{Path: r.URL.Path, Msg: m, Auth: r.Header.Get("Authorization")})
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return &Ntfy{
		TopicURL: srv.URL + "/backups-abc123",
		Machine:  "my-laptop",
		Client:   srv.Client(),
	}, &got
}

// The ordinary case. A phone should get something a person can act on from the
// lock screen without opening anything.
func TestNtfyCarriesTheAlert(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)
	err := n.Notify(context.Background(), Critical, "card is stale", "Backups have not reached it for 3 days.")
	if err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("server got %d messages, want 1", len(*got))
	}
	rec := (*got)[0]
	// Published to the server ROOT with the topic in the body — the JSON form.
	// Posting to /backups-abc123 instead would make ntfy treat the JSON as the
	// message text and put a wall of braces on the phone.
	if rec.Path != "/" {
		t.Errorf("posted to %q, want / — the JSON publish form goes to the server root", rec.Path)
	}
	if rec.Msg.Topic != "backups-abc123" {
		t.Errorf("topic = %q, want backups-abc123", rec.Msg.Topic)
	}
	// The machine name rides in the title, which is the line that survives
	// truncation on a lock screen.
	if !strings.Contains(rec.Msg.Title, "my-laptop") || !strings.Contains(rec.Msg.Title, "card is stale") {
		t.Errorf("title = %q, want the machine and the fault", rec.Msg.Title)
	}
	if rec.Msg.Message == "" {
		t.Error("no message: the phone would show ntfy's own placeholder")
	}
	if rec.Msg.Priority != 5 {
		t.Errorf("priority = %d, want 5 — a stopped backup is what this exists to interrupt somebody about", rec.Msg.Priority)
	}
}

// Normal urgency must not arrive dressed as an emergency. An all-clear or a
// pairing request that buzzes through a phone's quiet hours teaches the user to
// turn the whole topic off.
func TestNtfyDoesNotSendOrdinaryNewsAtMaxPriority(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)
	if err := n.Notify(context.Background(), Normal, "card is back", "Backups have resumed."); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if p := (*got)[0].Msg.Priority; p >= 5 {
		t.Errorf("priority = %d for ordinary news; the user will mute the topic", p)
	}
}

// THE WHOLE POINT OF MINIMAL MODE, and it matters more here than on the
// webhook: an ntfy topic is a name, not a password, and whoever guesses it
// reads everything sent to it.
func TestNtfyMinimalModeTellsTheServerNothingAboutTheHousehold(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)
	n.Minimal = true

	err := n.Notify(context.Background(), Critical,
		"nas-attic is stale", "Backups to /mnt/photos have not arrived for 3 days.")
	if err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	raw, _ := json.Marshal((*got)[0].Msg)
	for _, secret := range []string{"nas-attic", "my-laptop", "/mnt/photos", "photos"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("minimal mode leaked %q to the ntfy server: %s", secret, raw)
		}
	}
	// It still has to be worth sending.
	if (*got)[0].Msg.Priority != 5 {
		t.Error("severity was lost, so the phone will not treat it as urgent")
	}
	if (*got)[0].Msg.Message == "" {
		t.Error("nothing to display: the phone would buzz with an empty notification")
	}
}

// An empty body must not reach ntfy as an empty message — ntfy substitutes its
// own placeholder, which reads on the phone as a fault in backup-maker.
func TestNtfyNeverSendsAnEmptyMessage(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)
	if err := n.Notify(context.Background(), Normal, "a machine wants to pair", ""); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if strings.TrimSpace((*got)[0].Msg.Message) == "" {
		t.Error("an empty message was sent; ntfy will fill it in with something of its own")
	}
}

func TestNtfyTokenIsSentAsABearerToken(t *testing.T) {
	n, got := fakeNtfy(t, http.StatusOK)
	n.Token = "tk_secret"
	if err := n.Notify(context.Background(), Normal, "hello", "there"); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	if (*got)[0].Auth != "Bearer tk_secret" {
		t.Errorf("Authorization = %q, want a bearer token", (*got)[0].Auth)
	}
}

// A protected topic refusing the publish is the commonest misconfiguration, and
// the fix is specific. "403" alone sends somebody hunting the wrong thing.
func TestNtfyNamesAnAuthFailureRatherThanJustTheStatus(t *testing.T) {
	n, _ := fakeNtfy(t, http.StatusForbidden)
	err := n.Notify(context.Background(), Normal, "hello", "there")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("the error does not point at the fix: %v", err)
	}
}

func TestNtfyReportsARefusingServerAsAFailure(t *testing.T) {
	n, _ := fakeNtfy(t, http.StatusInternalServerError)
	err := n.Notify(context.Background(), Normal, "hello", "there")
	if err == nil {
		t.Fatal("a 500 from the server was reported as success")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

func TestAnUnconfiguredNtfySaysSoRatherThanPublishing(t *testing.T) {
	var n Ntfy
	if err := n.Notify(context.Background(), Normal, "x", ""); err == nil {
		t.Fatal("an empty topic was treated as deliverable")
	}
}

// Splitting the topic URL is this program's problem, not the user's: they paste
// what the ntfy app shows them, and every reasonable shape of that has to work.
func TestNtfyTopicURLSplitting(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantBase string
		wantTop  string
	}{
		{"the public server", "https://ntfy.sh/my-topic", "https://ntfy.sh/", "my-topic"},
		{"no scheme typed", "ntfy.sh/my-topic", "https://ntfy.sh/", "my-topic"},
		{"a trailing slash", "https://ntfy.sh/my-topic/", "https://ntfy.sh/", "my-topic"},
		{"self-hosted", "https://ntfy.example.com/alerts", "https://ntfy.example.com/", "alerts"},
		// A self-hosted instance behind a reverse proxy on a sub-path. The
		// server root is the sub-path, NOT the domain — posting to the domain
		// would reach whatever else is hosted there.
		{"self-hosted under a path", "https://example.com/ntfy/alerts", "https://example.com/ntfy/", "alerts"},
		{"plain http", "http://192.168.1.9:8080/alerts", "http://192.168.1.9:8080/", "alerts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, topic, err := Ntfy{TopicURL: c.in}.endpoint()
			if err != nil {
				t.Fatalf("%q was rejected: %v", c.in, err)
			}
			if base.String() != c.wantBase {
				t.Errorf("base = %q, want %q", base.String(), c.wantBase)
			}
			if topic != c.wantTop {
				t.Errorf("topic = %q, want %q", topic, c.wantTop)
			}
		})
	}
}

// A TOPIC IS NEVER INVENTED. Defaulting a bare server address to some topic
// name would publish a household's alerts to a name somebody else may already
// be subscribed to.
func TestNtfyRefusesAnAddressWithNoTopic(t *testing.T) {
	for _, in := range []string{"https://ntfy.sh", "https://ntfy.sh/", "my-topic"} {
		if _, _, err := (Ntfy{TopicURL: in}).endpoint(); err == nil {
			t.Errorf("%q was accepted; a topic was invented from nothing", in)
		}
	}
}

// Credentials in the address, which is how some self-hosted instances behind a
// reverse proxy expect to be reached. They must not be left in the URL that is
// posted to.
func TestNtfyCarriesBasicAuthFromTheAddress(t *testing.T) {
	var auth string
	var seenURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		seenURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	n := Ntfy{TopicURL: "http://phil:hunter2@" + host + "/alerts", Client: srv.Client()}

	if err := n.Notify(context.Background(), Normal, "hello", "there"); err != nil {
		t.Fatalf("delivery failed: %v", err)
	}
	user, pass, ok := (&http.Request{Header: http.Header{"Authorization": {auth}}}).BasicAuth()
	if !ok || user != "phil" || pass != "hunter2" {
		t.Errorf("basic auth did not survive the address: %q", auth)
	}
	if strings.Contains(seenURL, "hunter2") {
		t.Errorf("the password was left in the request line: %q", seenURL)
	}
}
