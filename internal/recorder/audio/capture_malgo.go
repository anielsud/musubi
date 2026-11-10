package audio

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"

	"github.com/gen2brain/malgo"
)

// deviceCapturer records from one miniaudio device. The device callback
// appends to buf on miniaudio's thread; everything else reads it under mu.
type deviceCapturer struct {
	mctx *malgo.AllocatedContext
	dev  *malgo.Device

	mu  sync.Mutex
	buf []float32
}

// freeContext releases a miniaudio context that is no longer needed.
func freeContext(c *malgo.AllocatedContext) {
	_ = c.Uninit()
	c.Free()
}

// isMonitorName reports whether a device name is a PulseAudio loopback,
// which the backend lists among ordinary inputs.
func isMonitorName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "monitor of ") || strings.Contains(n, ".monitor")
}

func listDevices() ([]DeviceInfo, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("initializing audio context: %w", err)
	}
	defer freeContext(mctx)

	found, err := mctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("enumerating capture devices: %w", err)
	}
	out := make([]DeviceInfo, 0, len(found))
	for _, d := range found {
		info := DeviceInfo{ID: d.ID.String(), Name: d.Name(), IsDefault: d.IsDefault != 0}
		if isMonitorName(info.Name) {
			info.DeviceType = Monitor
		}
		out = append(out, info)
	}
	return out, nil
}

func newCapturer(device string, deviceType DeviceType) (Capturer, error) {
	// Monitor sources only exist under PulseAudio (PipeWire serves them
	// through its PulseAudio layer), so don't let miniaudio pick ALSA.
	var only []malgo.Backend
	if deviceType == Monitor {
		only = []malgo.Backend{malgo.BackendPulseaudio}
	}
	mctx, err := malgo.InitContext(only, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("initializing audio context: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = 1
	deviceConfig.SampleRate = CaptureSampleRate
	// PipeWire's ALSA plugin misbehaves with mmap'd buffers.
	deviceConfig.Alsa.NoMMap = 1

	if device != "" && device != "default" {
		id, err := findDevice(mctx, device)
		if err != nil {
			freeContext(mctx)
			return nil, err
		}
		deviceConfig.Capture.DeviceID = id.Pointer()
	}

	c := &deviceCapturer{mctx: mctx}
	dev, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{Data: c.onData})
	if err != nil {
		freeContext(mctx)
		return nil, fmt.Errorf("initializing capture device: %w", err)
	}
	c.dev = dev
	return c, nil
}

// findDevice looks a capture device up by name or by ID. The returned ID
// is a copy the caller owns, so the pointer handed to miniaudio stays
// valid after the device list is gone.
func findDevice(mctx *malgo.AllocatedContext, device string) (*malgo.DeviceID, error) {
	found, err := mctx.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("enumerating devices: %w", err)
	}
	for _, d := range found {
		if d.Name() == device || d.ID.String() == device {
			id := d.ID
			return &id, nil
		}
	}
	return nil, fmt.Errorf("audio device not found: %s", device)
}

// onData turns each period of signed 16-bit little-endian frames into
// float32 in [-1, 1). A short buffer limits the count to whole samples.
func (c *deviceCapturer) onData(_, in []byte, frames uint32) {
	n := min(int(frames), len(in)/2)
	c.mu.Lock()
	defer c.mu.Unlock()
	for off := 0; off < 2*n; off += 2 {
		v := int16(binary.LittleEndian.Uint16(in[off:]))
		c.buf = append(c.buf, float32(v)/32768.0)
	}
}

func (c *deviceCapturer) Start() error { return c.dev.Start() }

func (c *deviceCapturer) Stop() error { return c.dev.Stop() }

func (c *deviceCapturer) Samples() []float32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append(make([]float32, 0, len(c.buf)), c.buf...)
}

func (c *deviceCapturer) Reset() {
	c.mu.Lock()
	c.buf = c.buf[:0]
	c.mu.Unlock()
}

func (c *deviceCapturer) Close() {
	if c.dev != nil {
		c.dev.Uninit()
		c.dev = nil
	}
	if c.mctx != nil {
		freeContext(c.mctx)
		c.mctx = nil
	}
}
