package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// HTTP side for one-click page update. The real download/verification/replacement logic is all in the selfupdate package.
// Here we are only responsible for authentication boundaries, concurrent mutual exclusion, progress broadcast, and telling main that "it's time to exit".
//
// Restart is not completed by this process: after temporarily saving the new version, the process exits with selfupdate.ExitRestart.
// Restarted by the daemon script (start.sh/start.bat, ENTRYPOINT under Docker).

// restartCh is closed after the upgrade is ready or the rollback is completed, and exits with ExitRestart after main is received.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested Returns a channel that was closed on "please exit and let the daemon pick me up again".
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState is the conclusion of selfupdate.Bootstrap during this startup (upgrade successful/just rolled back/
// The temporary file is discarded) and is injected by main for /api/update/check to truthfully tell the front end the outcome of the last upgrade.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState is called once by main at startup.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache caches the latest version query results of GitHub.
//
// The "new version available" prompt in the top bar will be checked every time the full page is loaded, while the unauthenticated GitHub API is
// 60 times per IP per hour - without caching, the quota will be exhausted by opening a few more tabs or refreshing the page.
// Later, when I really wanted to update, I couldn't find it. The user can force bypass the cache when explicitly clicking "Check for Updates".
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch is a fetching function, which is only an injection point reserved for testing; when it is nil, the real GitHub query will be executed.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// The failure results are also cached for a short time, otherwise when GitHub is unreachable, you will have to wait for a timeout every time the page is loaded;
	// But the TTL should be short, and it will recover soon after the network is restored.
	releaseErrTTL = 2 * time.Minute
	// The timeout used for queries. NewClient's 30-minute timeout is for downloading the entire package, and you cannot wait that long to check the version.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get returns the latest Release. If the cache is hit, the network will not be accessed.
//
// The lock is always held during retrieval: concurrent requests will be queued to wait for the results of one query, instead of going to GitHub individually.
// (When the page is first loaded, multiple tabs are checked at the same time, which is the most likely time to trigger current limiting).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// The request being canceled (the user closed the tab) does not mean there is a problem with GitHub. Don't write it to the cache.
	// Otherwise the next visitor will get an inexplicable "cancelled" error.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress is a progress pushed to the front end.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // Only the download phase is significant; the rest is -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub holds the progress of an upgrade and broadcasts it to SSE subscribers.
//
// running also acts as a mutex: POST /api/update/apply again during upgrade directly 409,
// Avoid two goroutines writing to the same artex.new at the same time.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin to seize the upgrade permission, and return false if it is already in progress.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "In preparation…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish ends an upgrade. err is nil, indicating that the temporary storage is successful and waiting for restart.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Update failed", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "The new version is ready, restarting...", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout must be called while holding h.mu. The subscriber channel is buffered and will be discarded when it is full——
// Progress is transient information that can be discarded, and a stuck SSE connection must not block the upgrade itself.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck queries the latest official version on GitHub and compares it with the current version.
//
// The front end will also be directly connected to api.github.com (GitHub's CORS is *), but **this interface shall prevail**:
// The download is done by the backend, and updates can only be made if the backend can access GitHub. The browser can connect, but the server cannot.
// The situation is very common (the server is on the intranet, or the agent is only configured on the browser), then the update will inevitably fail.
// It is better to report the error truthfully during the checking step.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// The top bar prompts to go to the cache (default); when the user clicks "Check for Updates", bring force=1 to force a return to the source.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// Development builds (dev/git describe with suffix) do not have comparable version numbers. Letting go will only
		// Use the official version to overwrite the local binary being debugged, so it will not be updated directly.
		out["reason"] = fmt.Sprintf("The current version %q is not an official release version and one-click update has been disabled", current)
	}
	writeJSON(w, 200, out)
}

// updateApply downloads and temporarily stores the new version. After completion, the process exits and is restarted by the daemon script.
//
// 202 is returned immediately, and the actual work is run on the background goroutine: it may take several minutes to download the entire package.
// Hanging on the request will be interrupted by the reverse generation timeout. The progress goes to /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// Go through the cache: Make sure that the version installed is the version that the user sees and confirms on the interface.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("The current version %q is not an official release version and one-click update has been disabled", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("It is currently the latest version %s", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "There is already an update in progress")
		return
	}

	go func() {
		// Deliberately use s.ctx instead of the request's ctx: the request ends as soon as the HTTP response comes back,
		// Downloads hanging on it will be canceled immediately.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] Update failed: %v", err)
			return
		}
		log.Printf("[update] %s → %s has been temporarily saved and will be exited to complete the change.", current, rel.TagName)
		// Leave some time to push the last piece of progress to the front end, and then trigger the exit.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback actively rolls back to the previous version (artex.old backed up before the change).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "The update is in progress and cannot be rolled back")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] Manually rolled back to the previous version and will exit to complete the switch")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream pushes update progress via SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// First add a current status. After refreshing the page, you can immediately see the upgrade in progress.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
