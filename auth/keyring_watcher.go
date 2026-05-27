package auth

import (
	"context"
	"os"
	"time"
)

// WatchKeyRingFile polls path's mtime every interval; when it changes,
// reload is called with the new contents. The function blocks until ctx
// is canceled. interval == 0 disables polling (returns immediately).
//
// onReload is invoked on every successful reload (after the snapshot
// swap). Errors during reload are passed to onError; the watcher keeps
// running so a transient parse failure does not kill the loop.
func WatchKeyRingFile(ctx context.Context, path string, ring *KeyRing, interval time.Duration, onReload func(activeID string), onError func(error)) {
	if interval <= 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		if onError != nil {
			onError(err)
		}
	}
	var lastMod time.Time
	if info != nil {
		lastMod = info.ModTime()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			info, err := os.Stat(path)
			if err != nil {
				if onError != nil {
					onError(err)
				}
				continue
			}
			if !info.ModTime().After(lastMod) {
				continue
			}
			lastMod = info.ModTime()
			if err := ReloadInto(path, ring); err != nil {
				if onError != nil {
					onError(err)
				}
				continue
			}
			if onReload != nil {
				onReload(ring.ActiveID())
			}
		}
	}
}
