//go:build !darwin

package meeting

import (
	"context"
	"errors"
)

// The recorder detects meetings on macOS only. Elsewhere the package
// still builds, for its platform names and title matching, but Start
// fails and nothing is ever listed.

func pulseInit() error {
	return errors.New("meeting detection is only available on macOS")
}

func pulseSubscribe() error                { return nil }
func pulseCleanup()                        {}
func pulseQuit()                           {}
func setActiveDetector(*Detector)          {}
func pulseEventLoop(ctx context.Context)   { <-ctx.Done() }
func pulseListSourceOutputs() []streamInfo { return nil }
func pulseListSinkInputs() []streamInfo    { return nil }
func getWindowTitleByPID(int) string       { return "" }
func processExists(int) bool               { return false }
func isSourceOutputNew(_, _ int) bool      { return false }
func isSourceOutputRemove(_, _ int) bool   { return false }
func isSinkInputNew(_, _ int) bool         { return false }
func isSinkInputRemove(_, _ int) bool      { return false }
