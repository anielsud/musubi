package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const list = `[
 {"tag_name":"v0.3.0-rc1","html_url":"https://github.com/o/r/releases/tag/v0.3.0-rc1","prerelease":true},
 {"tag_name":"v0.2.1","html_url":"https://github.com/o/r/releases/tag/v0.2.1"},
 {"tag_name":"v0.2.0","html_url":"https://github.com/o/r/releases/tag/v0.2.0"},
 {"tag_name":"v9.0.0","html_url":"https://github.com/o/r/releases/tag/v9.0.0","draft":true},
 {"tag_name":"v8.0.0","html_url":"https://evil.example/releases/v8.0.0"}
]`

func serve(t *testing.T) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("the check must be anonymous")
		}
		w.Write([]byte(list))
	}))
	t.Cleanup(s.Close)
	old := API
	API = s.URL
	t.Cleanup(func() { API = old })
}

func TestCheck(t *testing.T) {
	serve(t)
	cases := []struct {
		current string
		pre     bool
		want    string
	}{
		{"v0.2.0-rc2", false, "v0.2.1"},
		{"v0.2.0", false, "v0.2.1"},
		{"v0.2.1", false, ""},
		{"v0.2.1", true, "v0.3.0-rc1"},
		{"v0.3.0-rc1", true, ""},
		{"v0.2.0-rc2-3-gabc123", false, ""}, // a development build
		{"dev", true, ""},
	}
	for _, c := range cases {
		r, err := Check(context.Background(), http.DefaultClient, "o/r", c.current, c.pre)
		if err != nil {
			t.Fatalf("%s: %v", c.current, err)
		}
		got := ""
		if r != nil {
			got = r.Version
		}
		if got != c.want {
			t.Errorf("Check(%s, pre=%v) = %q, want %q", c.current, c.pre, got, c.want)
		}
	}
}

func TestCheckRejectsBadRepo(t *testing.T) {
	if _, err := Check(context.Background(), http.DefaultClient, "o/r/../x", "v0.1.0", false); err == nil {
		t.Error("want an error for a repo that isn't owner/name")
	}
}

func TestOrder(t *testing.T) {
	order := []string{"v0.1.0", "v0.2.0-rc1", "v0.2.0-rc2", "v0.2.0-rc10", "v0.2.0", "v0.2.1", "v0.10.0", "v1.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := parse(order[i-1])
		b, ok := parse(order[i])
		if !ok || !b.newer(a) || a.newer(b) {
			t.Errorf("%s should be newer than %s", order[i], order[i-1])
		}
	}
}
