// Package updates asks GitHub whether a newer Musubi has been released.
//
// It reads the public releases-only repository ([updates] repo) with an
// anonymous request: no account, token, dataset name or anything else
// about the user goes with it. Check only reports a newer version;
// Install (install_darwin.go) replaces the app with it when the user asks.
package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Release is a published release newer than the running app.
type Release struct {
	Version    string    `json:"version"`
	URL        string    `json:"url"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published"`
	// DMG and its SHA-256 file, when the release has both; Install needs
	// them.
	DMG    string `json:"-"`
	SHA256 string `json:"-"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// API is GitHub's API root (a variable for tests).
var API = "https://api.github.com"

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Check returns the newest release of repo that is newer than current,
// or nil when there is none. Pre-releases count only with prereleases.
// A current version that isn't a release (a development build) is never
// offered anything.
func Check(ctx context.Context, client *http.Client, repo, current string, prereleases bool) (*Release, error) {
	cur, ok := parse(current)
	if !ok {
		return nil, nil
	}
	if !repoName.MatchString(repo) {
		return nil, fmt.Errorf("[updates] repo %q is not owner/name", repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+repo+"/releases?per_page=30", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Musubi")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var list []ghRelease
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, err
	}
	var best *Release
	var bestV version
	prefix := "https://github.com/" + repo + "/releases/"
	for _, r := range list {
		if r.Draft || (r.Prerelease && !prereleases) {
			continue
		}
		v, ok := parse(r.TagName)
		if !ok || !v.newer(cur) || (best != nil && !v.newer(bestV)) {
			continue
		}
		// The app opens this link; only the repo's own release pages.
		if !strings.HasPrefix(r.HTMLURL, prefix) {
			continue
		}
		best, bestV = &Release{Version: r.TagName, URL: r.HTMLURL, Prerelease: r.Prerelease, Published: r.PublishedAt}, v
		dl := prefix + "download/" + r.TagName + "/"
		dmg := "Musubi-" + r.TagName + ".dmg"
		for _, a := range r.Assets {
			switch {
			case a.Name == dmg && a.URL == dl+dmg:
				best.DMG = a.URL
			case a.Name == dmg+".sha256" && a.URL == dl+dmg+".sha256":
				best.SHA256 = a.URL
			}
		}
	}
	return best, nil
}

// version is vMAJOR.MINOR.PATCH with an optional -rcN.
type version struct {
	n  [3]int
	rc int // 0: a release, which sorts after every rc of it
}

var tag = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-rc(\d+))?$`)

// parse reads a release tag. Development builds (git describe output
// such as v0.2.0-rc2-3-gabc123, "dev", or -dirty) don't parse.
func parse(s string) (version, bool) {
	m := tag.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return version{}, false
	}
	var v version
	for i := 0; i < 3; i++ {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	if m[4] != "" {
		v.rc, _ = strconv.Atoi(m[4])
		if v.rc == 0 {
			return version{}, false
		}
	}
	return v, true
}

func (v version) newer(o version) bool {
	for i := 0; i < 3; i++ {
		if v.n[i] != o.n[i] {
			return v.n[i] > o.n[i]
		}
	}
	switch {
	case v.rc == o.rc:
		return false
	case v.rc == 0:
		return true
	case o.rc == 0:
		return false
	}
	return v.rc > o.rc
}
