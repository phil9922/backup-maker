// SPDX-License-Identifier: MIT

package syncthing

import (
	"context"
	"log/slog"
	"time"
)

// StreamEvents long-polls /rest/events and hands each event to fn until ctx
// is cancelled. Errors (engine restarting, etc.) back off and resume.
func StreamEvents(ctx context.Context, c *Client, log *slog.Logger, fn func(Event)) {
	// A dedicated client call path with a timeout above the long-poll window
	// is handled by Events' 60s server timeout vs the 30s http client — so
	// use a shorter poll window.
	const pollSec = 20
	var since int64
	for ctx.Err() == nil {
		events, err := c.Events(since, pollSec)
		if err != nil {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		for _, ev := range events {
			if ev.ID > since {
				since = ev.ID
			}
			fn(ev)
		}
	}
}
