package live

import "testing"

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"8e50411a-5b45-4706-94d6-e6bb1659ac53": true,
		"../../../etc/passwd":                  false,
		"8e50411a-5b45-4706-94d6-e6bb1659ac5/": false,
		"8E50411A-5B45-4706-94D6-E6BB1659AC53": false,
		"":                                     false,
	} {
		if got := validSessionID(id); got != want {
			t.Errorf("%q: got %v want %v", id, got, want)
		}
	}
}
