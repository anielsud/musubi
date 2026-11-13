package gui

import (
	"log"
	"os"
	"regexp"
	"strings"
)

// WailsLog sends the Wails runtime's warnings and errors to the app log.
// Wails recovers a panic in a call from the screen and logs it as an error
// (the app keeps running), so without this a broken call leaves no trace.
// Those lines quote the whole call, arguments included, which can hold a
// token (GatewayConnect): only the method name and the error are kept.
type WailsLog struct{}

var (
	wailsCallName = regexp.MustCompile(`"name":"([^"]*)"`)
	wailsCallArgs = regexp.MustCompile(`"args"`)
)

func wailsLine(level, msg string) {
	if wailsCallArgs.MatchString(msg) {
		name := "?"
		if m := wailsCallName.FindStringSubmatch(msg); m != nil {
			name = m[1]
		}
		errText := ""
		// The dispatcher writes "<message> -> <error>".
		if i := strings.LastIndex(msg, " -> "); i >= 0 {
			errText = msg[i+4:]
		}
		msg = "call " + name + " failed: " + errText
	}
	log.Printf("wails %s: %s", level, msg)
}

func (WailsLog) Print(string)     {}
func (WailsLog) Trace(string)     {}
func (WailsLog) Debug(string)     {}
func (WailsLog) Info(string)      {}
func (WailsLog) Warning(m string) { wailsLine("warning", m) }
func (WailsLog) Error(m string)   { wailsLine("error", m) }
func (WailsLog) Fatal(m string) {
	wailsLine("fatal", m) // as Wails's own logger: a fatal error ends the app
	os.Exit(1)
}
