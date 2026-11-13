package audio

// CaptureSampleRate is the sample rate every audio source in the
// pipeline is expected to deliver: Parakeet TDT's native input rate
// (internal/transcribe/parakeet.go's sampleRate const). Sources that
// don't natively capture at this rate (e.g. guestaudio's ScreenCaptureKit
// tap, which delivers 48kHz) must resample down to it themselves — see
// Resample in dsp.go.
const CaptureSampleRate = 16000

// DeviceType tells a microphone apart from a loopback of what the
// speakers play, because the two need different backends to open.
type DeviceType int

const (
	// Input is a microphone or other ordinary capture device.
	Input DeviceType = iota
	// Monitor is a PulseAudio/PipeWire loopback of system output.
	Monitor
)

// DeviceInfo is one capture device as the audio backend lists it. ID is
// stable for a device across runs; Name is what people pick in settings.
type DeviceInfo struct {
	ID         string
	Name       string
	IsDefault  bool
	DeviceType DeviceType
}

// Capturer is a running capture device. It buffers everything it records
// until Reset, so a poller can take samples at its own pace; Samples
// returns a copy of that buffer as 16 kHz mono float32.
type Capturer interface {
	Start() error
	Stop() error
	Samples() []float32
	Reset()
	Close()
}

// ListDevices lists the capture devices the audio backend can open.
func ListDevices() ([]DeviceInfo, error) { return listDevices() }

// NewCapturer opens device (its name or ID; "" or "default" for the
// system default) without starting it.
func NewCapturer(device string, deviceType DeviceType) (Capturer, error) {
	return newCapturer(device, deviceType)
}
