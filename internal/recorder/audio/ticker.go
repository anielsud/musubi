package audio

import "time"

// captureTick is how often StreamCapturer collects what the device has
// buffered: about 256 samples at 16 kHz, half a VAD window, so windows
// leave with little delay without waking the loop needlessly.
const captureTick = 16 * time.Millisecond

// tickerIface is what the poll loop needs from a ticker. Tests swap in one
// they fire by hand, through makeTickerFunc, to step the loop exactly.
type tickerIface interface {
	Ticks() <-chan time.Time
	Stop()
}

type wallTicker struct{ *time.Ticker }

func (w wallTicker) Ticks() <-chan time.Time { return w.C }

var makeTickerFunc = func() tickerIface { return wallTicker{time.NewTicker(captureTick)} }
