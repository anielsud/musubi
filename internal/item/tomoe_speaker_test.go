package item

import "testing"

func TestTomoeSpeaker(t *testing.T) {
	for label, want := range map[string]struct {
		name         string
		owner, known bool
	}{
		"You":                      {"You", true, true},
		"Person 3":                 {"Person 3", false, false},
		"Person 3 (Ilse Novak)":    {"Ilse Novak", false, true},
		"Person 2 (Room - Oslo 4)": {"Room - Oslo 4", false, false},
		"Ilse Novak":               {"Ilse Novak", false, true}, // named by the user in Tomoe
		"New speaker":              {"New speaker", false, false},
		"Ana?":                     {"Ana?", false, false},
		"Harbour Zoom Room 1":      {"Harbour Zoom Room 1", false, false},
	} {
		n, o, k := tomoeSpeaker(label)
		if n != want.name || o != want.owner || k != want.known {
			t.Errorf("%q: %q %v %v, want %+v", label, n, o, k, want)
		}
	}
}
