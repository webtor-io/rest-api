package services

import (
	"sync"
	"time"
)

const verifyPerHour = 20 // tracker checks per uid an hour

// verifyLimiter caps the tracker checks of a uid at verifyPerHour in any
// hour. Nothing per hash: one user's checks of many hashes count together.
// ponytail: per replica (3 replicas → 3×); shared state if abuse shows up
type verifyLimiter struct {
	mu sync.Mutex
	at map[string][]time.Time // per uid: times of its checks in the last hour, oldest first
}

func newVerifyLimiter() *verifyLimiter { return &verifyLimiter{at: map[string][]time.Time{}} }

// allow reports whether uid may make one more check at now, and counts it if so.
func (l *verifyLimiter) allow(uid string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	since := now.Add(-time.Hour)
	if len(l.at) > 10000 { // uids that stopped checking leave
		for u, ts := range l.at {
			if !ts[len(ts)-1].After(since) {
				delete(l.at, u)
			}
		}
	}
	ts := l.at[uid]
	for len(ts) > 0 && !ts[0].After(since) {
		ts = ts[1:]
	}
	if len(ts) >= verifyPerHour {
		l.at[uid] = ts
		return false
	}
	l.at[uid] = append(ts, now)
	return true
}
