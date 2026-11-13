package meeting

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// EventType says whether a MeetingEvent opens or closes a call.
type EventType int

const (
	MeetingStarted EventType = iota
	MeetingStopped
)

// MeetingEvent is what the detector reports when a call begins or ends,
// with the app it was identified as.
type MeetingEvent struct {
	Type     EventType
	Platform Platform
}

// streamInfo is one app's microphone or playback stream as the platform
// layer lists it. Only PID ties a mic stream to a playback stream; the
// name is what identifyPlatform reads.
type streamInfo struct {
	Index   uint32
	AppName string
	PID     int
}

// callInProgress is the call the detector has confirmed and is watching
// for its end.
type callInProgress struct {
	pid      int
	platform Platform
}

// Detector reports meeting starts and stops. A call is one process that
// holds a microphone stream and a playback stream at the same time: music
// players only play and dictation only records, so the pair is what sets a
// call apart. The platform layer (detect_darwin.go) supplies the stream
// lists and change notifications; the rules here are shared.
type Detector struct {
	events chan MeetingEvent

	mu      sync.Mutex
	current *callInProgress
	stopped bool

	// candidatePID is a process seen holding both streams that has not yet
	// held them for debounceDelay; candidateSince is when it was first seen.
	candidatePID   int
	candidateSince time.Time

	cancel context.CancelFunc
}

// NewDetector returns a detector that is not yet watching anything.
func NewDetector() *Detector {
	// A little buffer so a start and stop in quick succession both survive
	// a slow reader; beyond that, events are dropped rather than blocking
	// the stream callbacks.
	return &Detector{events: make(chan MeetingEvent, 4)}
}

// Events is where starts and stops are delivered.
func (d *Detector) Events() <-chan MeetingEvent { return d.events }

// Start hooks the detector to the platform's stream notifications and
// returns once they are running; the watching itself happens in background
// goroutines until ctx ends or Stop is called.
func (d *Detector) Start(ctx context.Context) error {
	d.mu.Lock()
	ctx, d.cancel = context.WithCancel(ctx)
	d.mu.Unlock()

	if err := pulseInit(); err != nil {
		return fmt.Errorf("audio stream monitor init failed: %w", err)
	}
	// Notifications are routed to whichever detector registered last.
	setActiveDetector(d)
	if err := pulseSubscribe(); err != nil {
		pulseCleanup()
		return fmt.Errorf("audio stream monitor subscribe failed: %w", err)
	}

	go func() {
		defer pulseCleanup()
		pulseEventLoop(ctx)
	}()
	go d.healthCheckLoop(ctx)

	// The event loop only reports apps that start or stop audio from now
	// on, so a meeting already under way when the app launches (or
	// relaunches mid-call) would never be noticed. Look once now; the
	// usual debounce still applies.
	go d.checkForMeeting()

	fmt.Println("meeting: detector running")
	return nil
}

// Stop ends detection. Events already in the channel stay there.
func (d *Detector) Stop() {
	// Deregister before anything else: if this ran after a later Start had
	// registered a new detector, the late clear would silence that one.
	setActiveDetector(nil)

	d.mu.Lock()
	d.stopped = true
	cancel := d.cancel
	d.mu.Unlock()

	// The event loop may be blocked waiting for the platform; wake it so
	// it sees the cancelled context.
	pulseQuit()
	if cancel != nil {
		cancel()
	}
}

// debounceDelay is how long a process must keep both streams before it
// counts as a call. Apps open and close streams in bursts while a call
// sets up, and those brief pairs should not start a recording.
const debounceDelay = 2 * time.Second

// healthCheckInterval is how often the tracked process is checked for
// having exited without its streams being reported gone.
const healthCheckInterval = 30 * time.Second

// onSubscribeEvent turns a stream change from the platform layer into a
// check. Any new stream may complete a mic+playback pair; any removed
// stream may be the end of the current call. The checks run on their own
// goroutines so a platform query never holds up the notification source.
func (d *Detector) onSubscribeEvent(facility, eventType int, _ uint32) {
	d.mu.Lock()
	stopped := d.stopped
	d.mu.Unlock()
	if stopped {
		return
	}

	if isSourceOutputNew(facility, eventType) || isSinkInputNew(facility, eventType) {
		go d.checkForMeeting()
		return
	}
	if isSourceOutputRemove(facility, eventType) || isSinkInputRemove(facility, eventType) {
		go d.checkForMeetingEnd()
	}
}

// firstCaller is the first mic stream, in listing order, whose process is
// also playing audio.
func firstCaller(mics, speakers []streamInfo) (streamInfo, bool) {
	playing := make(map[int]bool, len(speakers))
	for _, s := range speakers {
		if s.PID > 0 {
			playing[s.PID] = true
		}
	}
	for _, m := range mics {
		if m.PID > 0 && playing[m.PID] {
			return m, true
		}
	}
	return streamInfo{}, false
}

// checkForMeeting looks for a process holding both streams and moves it
// through the debounce: the first sighting makes it the candidate and
// schedules a second look debounceDelay later; a sighting of the same
// candidate after that confirms the call and emits MeetingStarted.
func (d *Detector) checkForMeeting() {
	d.mu.Lock()
	idle := d.current == nil && !d.stopped
	d.mu.Unlock()
	if !idle {
		return
	}

	mics := pulseListSourceOutputs()
	speakers := pulseListSinkInputs()
	caller, found := firstCaller(mics, speakers)
	if !found {
		// Nothing has both streams any more: a pending detection died before
		// its recheck. Left set, it would stop the next detection from
		// scheduling its own recheck.
		d.mu.Lock()
		d.candidatePID = 0
		d.mu.Unlock()
		return
	}

	d.mu.Lock()
	if d.current != nil {
		// Another check confirmed a call while this one was listing.
		d.mu.Unlock()
		return
	}
	now := time.Now()

	if d.candidatePID != caller.PID {
		// A new candidate. Only one recheck is ever outstanding: if one is
		// already scheduled for an earlier candidate, it serves this one
		// too, and the candidate's clock restarts from now.
		scheduleRecheck := d.candidatePID == 0
		d.candidatePID = caller.PID
		d.candidateSince = now
		d.mu.Unlock()
		if scheduleRecheck {
			go func() {
				time.Sleep(debounceDelay)
				d.checkForMeeting()
			}()
		}
		return
	}

	if now.Sub(d.candidateSince) < debounceDelay {
		// Seen again too soon; the scheduled recheck will decide.
		d.mu.Unlock()
		return
	}

	platform := identifyPlatform(caller.AppName, caller.PID)
	d.current = &callInProgress{pid: caller.PID, platform: platform}
	d.candidatePID = 0
	d.mu.Unlock()

	fmt.Printf("meeting: %s call started, process %d\n", platform, caller.PID)
	d.emit(MeetingEvent{Type: MeetingStarted, Platform: platform})
}

// checkForMeetingEnd ends the current call once its process no longer
// holds a microphone stream; a playback stream that outlives it does not
// keep the call open.
func (d *Detector) checkForMeetingEnd() {
	d.mu.Lock()
	call := d.current
	stopped := d.stopped
	d.mu.Unlock()
	if call == nil || stopped {
		return
	}
	pid, platform := call.pid, call.platform

	for _, m := range pulseListSourceOutputs() {
		if m.PID == pid {
			return
		}
	}

	d.mu.Lock()
	if d.current == nil || d.current.pid != pid {
		// Already ended, or replaced, by a concurrent check.
		d.mu.Unlock()
		return
	}
	d.current = nil
	d.mu.Unlock()

	fmt.Printf("meeting: %s call over, process %d\n", platform, pid)
	d.emit(MeetingEvent{Type: MeetingStopped, Platform: platform})
}

// emit delivers ev unless the channel is full, in which case it is logged
// and dropped: blocking here would stall every later check.
func (d *Detector) emit(ev MeetingEvent) {
	select {
	case d.events <- ev:
	default:
		kind := "start"
		if ev.Type == MeetingStopped {
			kind = "stop"
		}
		fmt.Printf("meeting: no room for %s event, dropped\n", kind)
	}
}

// healthCheckLoop catches a call whose process died (crashed or was
// killed) without the platform reporting its streams removed.
func (d *Detector) healthCheckLoop(ctx context.Context) {
	tick := time.NewTicker(healthCheckInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		d.mu.Lock()
		call := d.current
		d.mu.Unlock()
		if call == nil || processExists(call.pid) {
			continue
		}
		fmt.Printf("meeting: process %d of the current call is gone\n", call.pid)
		d.checkForMeetingEnd()
	}
}
