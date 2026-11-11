package m365

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Keychain keeps the secret as a generic password in the login Keychain
// through /usr/bin/security, the way Musubi's other keys are kept
// ("keychain:<service>" in the config). The item is created by Apple's
// signed security tool, so its access list names that tool: the app and
// the CLI read it through the same tool without a prompt, rebuild after
// rebuild. (Created through the Security framework, the item named the
// creating binary, and Musubi's locally signed builds made macOS ask on
// every launch.)
//
// The value goes in base64 as an argument (not through `security -i`,
// whose input lines are cut at about 4 KB). It is visible to the owner's
// own processes for the moment the tool runs; those processes could read
// the item through the tool anyway.
type Keychain struct{ Service, Account string }

func (k Keychain) Get() ([]byte, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", k.Account, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
			return nil, ErrNotConnected
		}
		return nil, fmt.Errorf("keychain: %w", err)
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func (k Keychain) Put(b []byte) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", k.Service, "-a", k.Account, "-l", "Musubi Microsoft 365 sign-in", "-w", base64.StdEncoding.EncodeToString(b))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (k Keychain) Delete() error {
	err := exec.Command("security", "delete-generic-password", "-s", k.Service, "-a", k.Account).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return ErrNotConnected
	}
	return err
}
