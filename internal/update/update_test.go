// SPDX-License-Identifier: MIT

package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewerComparesReleases(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
		why             string
	}{
		{"0.1.6", "0.1.7", true, "a patch release is newer"},
		{"0.1.6", "0.2.0", true, "a minor release is newer"},
		{"0.9.9", "1.0.0", true, "a major release is newer"},
		{"v0.1.6", "v0.1.7", true, "a leading v on both sides is fine"},
		{"0.1.6", "v0.1.7", true, "mixed v prefixes are fine"},
		{"0.1.6", "0.1.6", false, "the same version is not an update"},
		{"0.1.7", "0.1.6", false, "an older release is not an update"},
		{"0.2.0", "0.1.9", false, "minor beats patch when comparing"},
		{"1.0.0", "0.9.9", false, "major beats minor when comparing"},
		// The ordering bug this pins: string comparison would call 0.1.10
		// older than 0.1.9, so the tenth patch release would never be offered.
		{"0.1.9", "0.1.10", true, "0.1.10 is newer than 0.1.9, not older"},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v — %s", c.current, c.latest, got, c.want, c.why)
		}
	}
}

// A BUILD FROM SOURCE MUST STAY QUIET. Its version is "dev", which is not a
// release and cannot be compared with one. Announcing an update to a developer
// running their own working tree is noise, and — worse — it would fire on every
// contributor's machine the moment they built the project.
func TestABuildWithNoReleaseVersionIsNeverToldToUpdate(t *testing.T) {
	for _, current := range []string{"dev", "dev-dirty", "", "unknown"} {
		if Newer(current, "9.9.9") {
			t.Errorf("a %q build was told to update", current)
		}
	}
}

// A SNAPSHOT IS NOT THE RELEASE IT IS NAMED AFTER. goreleaser calls local
// snapshots "0.1.7-next"; treating that as 0.1.7 would announce a release that
// does not exist, and then stay silent when the real 0.1.7 arrived.
func TestAPreReleaseIsNotTreatedAsItsRelease(t *testing.T) {
	if Newer("0.1.6", "0.1.7-next") {
		t.Error("a -next snapshot was announced as a release")
	}
	if Newer("0.1.7-next", "0.1.7") {
		t.Error("a snapshot build compared as if it were a release")
	}
	if Newer("0.1.6", "0.2.0-rc1") {
		t.Error("a release candidate was offered as an update")
	}
}

// Anything unparseable means "no update", never "yes". A wrong yes sends
// somebody looking for a release that is not there.
func TestNonsenseVersionsNeverAnnounceAnUpdate(t *testing.T) {
	for _, latest := range []string{"", "latest", "v", "1.2", "1.2.3.4", "a.b.c", "-1.0.0"} {
		if Newer("0.1.6", latest) {
			t.Errorf("latest=%q was treated as an update", latest)
		}
	}
}

func TestLatestReadsTheTagFromGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request must carry nothing identifying about this machine.
		if q := r.URL.RawQuery; q != "" {
			t.Errorf("the check sent a query string: %q", q)
		}
		if a := r.Header.Get("Authorization"); a != "" {
			t.Errorf("the check sent credentials: %q", a)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.7","name":"v0.1.7"}`))
	}))
	t.Cleanup(srv.Close)

	got, err := Checker{URL: srv.URL, Client: srv.Client()}.Latest(context.Background())
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if got != "v0.1.7" {
		t.Errorf("got %q, want v0.1.7", got)
	}
}

// Rate limiting is named, because several machines behind one address share a
// budget and "403" alone sends somebody hunting the wrong thing.
func TestRateLimitingIsReportedAsItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := Checker{URL: srv.URL, Client: srv.Client()}.Latest(context.Background())
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "rate-limiting") {
		t.Errorf("the error does not name the cause: %v", err)
	}
}

func TestAnUnreachableOrOddAnswerIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":""}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (Checker{URL: srv.URL, Client: srv.Client()}).Latest(context.Background()); err == nil {
		t.Error("an empty tag was accepted")
	}
	if _, err := (Checker{URL: "http://127.0.0.1:9/nope"}).Latest(context.Background()); err == nil {
		t.Error("an unreachable host was reported as success")
	}
}
