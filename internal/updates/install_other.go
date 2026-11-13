//go:build !darwin

package updates

import (
	"context"
	"errors"
	"net/http"
)

var errMacOnly = errors.New("Musubi updates itself only on macOS")

func AppPath() (string, error) { return "", errMacOnly }

func Install(context.Context, *http.Client, *Release, string, func(string)) error { return errMacOnly }

func RemovePrevious(string) {}

func Relaunch(string, int) error { return errMacOnly }
