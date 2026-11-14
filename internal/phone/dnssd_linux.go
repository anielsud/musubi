package phone

import (
	"os"
	"strings"
)

// No dns-sd on linux: phone sync is a macOS feature for now.
func platformDNSSD(...string) (dnssdProc, error) { return nil, errDNSSDUnsupported }

func computerName() string {
	h, _ := os.Hostname()
	return strings.TrimSuffix(h, ".local")
}
