//go:build darwin && keychain

package m365

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Run with -tags keychain: it writes to the login Keychain.
func TestKeychainRoundTrip(t *testing.T) {
	k := Keychain{Service: "Musubi.M365.test", Account: "test"}
	defer k.Delete()
	val := []byte(strings.Repeat("refresh-token-like-value.", 400)) // ~10 KB, longer than any real token
	if err := k.Put(val); err != nil {
		t.Fatal(err)
	}
	got, err := k.Get()
	if err != nil || !bytes.Equal(got, val) {
		t.Fatalf("round trip: %d bytes, err %v", len(got), err)
	}
	if err := k.Put([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Get(); string(got) != "second" {
		t.Fatalf("update: %q", got)
	}
	if err := k.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get(); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("after delete: %v", err)
	}
}
