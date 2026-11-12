package recording

import (
	"fmt"

	"github.com/anielsud/musubi/internal/recorder/livefeed"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// Version is the build version (set by musubi-recorder run from its
// -ldflags version), reported in the live feed's hello message.
var Version = "dev"

// The live feed ([meeting] live_feed, see internal/livefeed) mirrors to a
// local socket what the transcript pane is sent: session starts, stops
// and saves, every new line and every revision of one. a.liveFeed is
// guarded by a.mu and nil while the feed is off, and the feed's methods do
// nothing on nil, so the call sites below cost nothing then.
//
// Lock order is a.mu, then the feed's own lock: segments are published
// while a.mu is held (right where they are applied to the session), and a
// new client's snapshot is taken and the client queued under a.mu too
// (liveFeedSnapshot), so a client sees every change exactly once, either
// in its snapshot or after it. That is safe because publishing never
// blocks -- it only tries to queue for each client, dropping a client
// that has fallen behind -- and the feed never takes a.mu itself except
// through liveFeedSnapshot. Stopping the feed waits for clients still
// being attached, so it must happen with a.mu released (stopLiveFeed).

// startLiveFeed starts the live feed if on and not already running.
func (a *Recorder) startLiveFeed(on bool) error {
	a.mu.Lock()
	running := a.liveFeed != nil
	a.mu.Unlock()
	if !on || running {
		return nil
	}
	srv, err := livefeed.Listen(livefeed.SocketPath(), livefeed.Options{Version: Version, Snapshot: a.liveFeedSnapshot})
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.liveFeed != nil { // started concurrently by a settings change
		a.mu.Unlock()
		srv.Close()
		return nil
	}
	a.liveFeed = srv
	a.mu.Unlock()
	fmt.Printf("live feed: serving %s\n", srv.Path())
	return nil
}

// stopLiveFeed stops the live feed if it's running, disconnecting its
// clients and removing the socket. Must be called without a.mu held.
func (a *Recorder) stopLiveFeed() {
	a.mu.Lock()
	srv := a.liveFeed
	a.liveFeed = nil
	a.mu.Unlock()
	if srv != nil {
		if err := srv.Close(); err != nil {
			fmt.Printf("live feed: closing: %v\n", err)
		}
	}
}

// liveFeedSnapshot is the feed's SnapshotFunc: the session recording now
// and its lines, attached under a.mu (see the lock order above).
func (a *Recorder) liveFeedSnapshot(attach livefeed.Attach) {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := livefeed.StatusMessage(a.statusLocked())
	if a.currentSess == nil {
		attach(nil, nil, status)
		return
	}
	info := livefeed.InfoFor(a.currentSess)
	attach(&info, a.currentSess.Segments, status)
}

// feedSessionLocked sends a session state change to the live feed.
// Caller holds a.mu.
func (a *Recorder) feedSessionLocked(state string, sess *session.Session) {
	if a.liveFeed != nil {
		a.liveFeed.Session(state, livefeed.InfoFor(sess))
	}
}

// feedSession is feedSessionLocked for callers not holding a.mu.
func (a *Recorder) feedSession(state string, sess *session.Session) {
	a.mu.Lock()
	a.feedSessionLocked(state, sess)
	a.mu.Unlock()
}

// feedSegmentsLocked sends lines of sess that were just added or changed
// to the live feed: the version sess now holds rather than seg as given,
// so the feed never goes backwards when a stale revision arrives after a
// newer one (UpsertSegment ignores it, see session.statusRank) or when a
// relabel computed earlier is delivered after a later revision of the
// same line. A line no longer in sess (withdrawn, StatusRemoved) is sent
// as given. Caller holds a.mu.
func (a *Recorder) feedSegmentsLocked(sess *session.Session, segs ...session.Segment) {
	if a.liveFeed == nil {
		return
	}
	for _, seg := range segs {
		a.liveFeed.Segment(sess.ID, currentVersion(sess, seg))
	}
}

// currentVersion is sess's line with seg's ID, or seg if there is none.
// Searched from the end, where new lines and the revisions they get
// while being spoken and refined are.
func currentVersion(sess *session.Session, seg session.Segment) session.Segment {
	for i := len(sess.Segments) - 1; i >= 0; i-- {
		if sess.Segments[i].ID == seg.ID {
			return sess.Segments[i]
		}
	}
	return seg
}
