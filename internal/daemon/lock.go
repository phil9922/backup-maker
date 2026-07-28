// SPDX-License-Identifier: MIT

package daemon

import (
	"fmt"

	"github.com/gofrs/flock"

	"github.com/phil9922/backup-maker/internal/config"
)

// acquireLock takes the single-instance lock, or fails fast if another daemon
// holds it. The returned release func must be called on shutdown.
func acquireLock() (release func(), err error) {
	path, err := config.LockPath()
	if err != nil {
		return nil, err
	}
	fl := flock.New(path)
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if !ok {
		return nil, fmt.Errorf("another backup-maker daemon is already running (lock held on %s)", path)
	}
	return func() { _ = fl.Unlock() }, nil
}
