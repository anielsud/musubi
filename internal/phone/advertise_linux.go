package phone

import (
	"os"
	"strings"
)

// No advertiser on linux: the phone uses a typed address there.
func platformAdvertise(string, int, []string) (advertProc, error) {
	return nil, errAdvertiseUnsupported
}

func computerName() string {
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}
