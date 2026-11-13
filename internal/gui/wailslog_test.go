package gui

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestWailsLogDropsCallArguments(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	const secret = "sk-test-do-not-log"
	WailsLog{}.Error(`process message error: C{"name":"main.App.GatewayConnect","args":["https://gw","me","` + secret + `"],"callbackID":"x"} -> runtime error: invalid memory address`)
	got := buf.String()
	if strings.Contains(got, secret) {
		t.Fatalf("log has the argument: %q", got)
	}
	want := "wails error: call main.App.GatewayConnect failed: runtime error: invalid memory address"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	buf.Reset()
	WailsLog{}.Info("ignored")
	WailsLog{}.Warning("asset not found")
	if got := buf.String(); strings.Contains(got, "ignored") || !strings.Contains(got, "wails warning: asset not found") {
		t.Fatalf("got %q", got)
	}
}
