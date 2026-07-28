// SPDX-License-Identifier: MIT

package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// checkTimeout bounds one check. Short on purpose: nothing about a backup may
// wait on GitHub, and a check that misses today runs again tomorrow.
const checkTimeout = 15 * time.Second

// LatestURL is where the newest published release is looked up.
//
// /releases/latest EXCLUDES PRE-RELEASES on GitHub's side, which is the
// behaviour wanted here — goreleaser publishes tags like v0.2.0-rc1 as
// prereleases, and nobody should be told to upgrade to a release candidate.
// parse() refuses them too, so it takes both GitHub and this package agreeing
// before anything is announced.
const LatestURL = "https://api.github.com/repos/phil9922/backup-maker/releases/latest"

// Checker asks GitHub what the newest release is.
//
// ONE HTTP GET, NO AUTH, NO IDENTIFIERS. The request carries a User-Agent and
// nothing else — no machine name, no version, no install id, no query string.
// GitHub learns that some IP asked a public repository a public question, which
// is the least that can be asked while still asking. Nothing about this
// household travels.
type Checker struct {
	// URL to query; empty means LatestURL. Exists so tests point at a recorder.
	URL string
	// Client to use; nil means a private one with the timeout above.
	Client *http.Client
}

// Latest returns the newest published release tag, e.g. "v0.1.6".
func (c Checker) Latest(ctx context.Context) (string, error) {
	url := c.URL
	if url == "" {
		url = LatestURL
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "backup-maker")

	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: checkTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach github.com: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		// Unauthenticated callers get 60 requests an hour per address. One
		// check a day cannot reach that alone, but several machines behind one
		// address share the budget — so it is named rather than reported as a
		// generic failure somebody would go hunting for.
		return "", fmt.Errorf("github.com is rate-limiting this address (%s); the next check will try again", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github.com answered %s", resp.Status)
	}
	// Bounded read: this is a public endpoint answering into a backup daemon,
	// and an unbounded decode from the internet is not something to leave lying
	// around even when the far end is trusted.
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(&limitedReader{r: resp.Body, n: 1 << 20}).Decode(&body); err != nil {
		return "", fmt.Errorf("could not read github.com's answer: %w", err)
	}
	tag := strings.TrimSpace(body.TagName)
	if tag == "" {
		return "", fmt.Errorf("github.com named no release")
	}
	return tag, nil
}

// limitedReader caps how much is read, reporting the cap as an error rather
// than silently truncating into a parser.
type limitedReader struct {
	r interface{ Read([]byte) (int, error) }
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("the answer from github.com is implausibly large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}
