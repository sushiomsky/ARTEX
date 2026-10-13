package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Repo is the release source. Hard-coded rather than made into configuration items: the update source can be allocated to anyone who can change the configuration.
// The remote code execution channel is not an opening for a penetration testing platform.
const Repo = "Autumn-27/artex"

// latestURL is GitHub's "latest official version" interface. It automatically skips prerelease and draft.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts limits the domain names that the upgrade link can access. In conjunction with the checkRedirect below,
// Any hop redirected to a host not on the list will fail directly - this is to prevent DNS pollution / man-in-the-middle
// The first gate to replace the binary, the second gate is SHA256SUMS comparison.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // The object storage where release assets are actually implemented
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release is the field we care about in GitHub Release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset is a file attached to Release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// NewClient constructs an HTTP client that only recognizes the GitHub domain name. If proxy is empty, the connection will be direct.
//
// Deliberately not reusing the default Transport: the upgrade link must be forced to use TLS and verify the certificate, and cannot be used elsewhere.
// Settings like InsecureSkipVerify are affected.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // Downloading the entire package cannot be blocked due to timeout at the request level.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("Too many redirects")
			}
			return checkURL(req.URL)
		},
	}
}

// checkURL forces https + domain name whitelisting.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("Reject non-HTTPS addresses: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("Reject non-GitHub domains: %s", u.Hostname())
	}
	return nil
}

// FetchLatest queries the latest official version.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to access GitHub (global proxy can be configured in system settings): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusTooManyRequests:
		// The unauthenticated GitHub API is 60 per hour per IP, which is easy to hit when sharing egress IPs.
		return nil, fmt.Errorf("GitHub interface current limit (60 times per hour), please try again later")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Repository %s has not released any official version yet", Repo)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub returns %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("Failed to parse Release: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release is missing tag")
	}
	return &rel, nil
}

// AssetName returns the release package name corresponding to the current platform, which is consistent with package_binary in build.sh:
// artex-<version>-<os>-<arch>.zip (version number without v prefix).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset finds assets by name in Release.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
