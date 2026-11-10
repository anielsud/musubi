package audio

// ListMonitorSources is ListDevices narrowed to the loopback sources that
// carry what the speakers play.
func ListMonitorSources() ([]DeviceInfo, error) {
	all, err := ListDevices()
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	for _, d := range all {
		if d.DeviceType == Monitor {
			out = append(out, d)
		}
	}
	return out, nil
}

// DefaultMonitorDevice names the loopback of the default output if there
// is one, else the first loopback listed, else "" (none, or listing failed).
func DefaultMonitorDevice() string {
	monitors, err := ListMonitorSources()
	if err != nil {
		return ""
	}
	name := ""
	for i, m := range monitors {
		if m.IsDefault {
			return m.Name
		}
		if i == 0 {
			name = m.Name
		}
	}
	return name
}
