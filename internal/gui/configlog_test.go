package gui

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

func TestEditConfigLogsNamesNotValues(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	path := filepath.Join(t.TempDir(), "config.toml")
	const secret = "sk-test-do-not-log"
	err := editConfig(path, []config.Edit{
		{Section: "gateway", Key: "api_key", Value: secret},
		{Section: "gateway", Key: "token_id", Delete: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, secret) {
		t.Fatalf("log has the value: %q", got)
	}
	if !strings.Contains(got, "gateway.api_key, gateway.token_id (removed)") {
		t.Fatalf("log does not name the keys: %q", got)
	}
}

func TestNotReadyCarriesTheCause(t *testing.T) {
	a := &App{}
	if err := a.notReady(); !strings.Contains(err.Error(), "no dataset open") {
		t.Fatalf("got %v", err)
	}
	a.err = "lock held by another process"
	if err := a.notReady(); err.Error() != "not ready: lock held by another process" {
		t.Fatalf("got %v", err)
	}
}
