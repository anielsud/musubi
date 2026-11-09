//go:build !darwin

package m365

import "errors"

// Keychain is macOS-only; elsewhere the sign-in can't be kept.
type Keychain struct{ Service, Account string }

var errNoKeychain = errors.New("keeping a Microsoft 365 sign-in needs the macOS Keychain")

func (Keychain) Get() ([]byte, error) { return nil, ErrNotConnected }
func (Keychain) Put([]byte) error     { return errNoKeychain }
func (Keychain) Delete() error        { return ErrNotConnected }
