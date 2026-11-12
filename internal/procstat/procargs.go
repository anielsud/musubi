package procstat

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// parseProcArgs reads macOS's kern.procargs2 buffer. Here rather than in
// the darwin file so its test runs on any system.
func parseProcArgs(b []byte) ([]string, error) {
	if len(b) < 4 {
		return nil, errors.New("procargs too short")
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	// Skip the executable path and the padding after it.
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil, errors.New("procargs: no executable path")
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	// A cap, so a damaged buffer can't make us allocate without bound.
	if argc < 0 || argc > 4096 {
		return nil, errors.New("procargs: bad argc")
	}
	args := make([]string, 0, argc)
	for len(args) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			args = append(args, string(b))
			break
		}
		args = append(args, string(b[:j]))
		b = b[j+1:]
	}
	return args, nil
}
