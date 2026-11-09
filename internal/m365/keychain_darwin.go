package m365

import (
	"errors"
	"fmt"

	"github.com/keybase/go-keychain"
)

// Keychain keeps the secret as a generic password in the login Keychain,
// through the Security framework: no size limit, and the value never
// passes through a command line.
type Keychain struct{ Service, Account string }

func (k Keychain) query() keychain.Item {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(k.Service)
	q.SetAccount(k.Account)
	return q
}

func (k Keychain) Get() ([]byte, error) {
	q := k.query()
	q.SetMatchLimit(keychain.MatchLimitOne)
	q.SetReturnData(true)
	res, err := keychain.QueryItem(q)
	if err != nil {
		return nil, fmt.Errorf("keychain: %w", err)
	}
	if len(res) == 0 {
		return nil, ErrNotConnected
	}
	return res[0].Data, nil
}

func (k Keychain) Put(b []byte) error {
	item := k.query()
	item.SetLabel("Musubi Microsoft 365 sign-in")
	item.SetData(b)
	item.SetAccessible(keychain.AccessibleAfterFirstUnlockThisDeviceOnly)
	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		upd := keychain.NewItem()
		upd.SetData(b)
		err = keychain.UpdateItem(k.query(), upd)
	}
	if err != nil {
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}

func (k Keychain) Delete() error {
	err := keychain.DeleteItem(k.query())
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return ErrNotConnected
	}
	return err
}
