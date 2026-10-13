package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// releaseCache is the layer that protects GitHub quotas: only 60 times/hour/IP for unauthenticated APIs,
// The "New version available" prompt in the top bar will be checked every time the entire page is loaded. Once the cache expires, users can open more
// The tab page will use up the quota, and then I really want to update it but I can't find it.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 queries should only return to the source once, but the actual number is %d times", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Users who click "Check for Updates" must get real-time results, otherwise the newly released version will not be visible until the cache expires.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force should bypass the cache, expected to return to the origin 2 times, actual %d times", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// Move the drop-in time forward to just after expiration to simulate the TTL arrival point.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("After the TTL expires, it should be returned to the source. Expected 2 times, actual %d times", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github is not reachable")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expect an error to be returned")
	}
	// The failure results also need to be cached for a while, otherwise when GitHub is unreachable, it will wait in vain for a timeout every time the page is loaded.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expect an error to be returned")
	}
	if calls != 1 {
		t.Errorf("The error should be cached for a short time. Expected to be returned to the source 1 times, actual %d times", calls)
	}

	// However, the error TTL must be significantly shorter than the successful one, and the network must be able to heal itself quickly after recovery.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("Error TTL(%v) must be shorter than success TTL(%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("Expect an error to be returned")
	}
	if calls != 2 {
		t.Errorf("Error should retry after TTL expired, expected 2 times, actual %d times", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// The request will be canceled if the visitor closes the tab. That doesn't mean there's a problem with GitHub, and you should never put "Cancelled"
	// Write to cache - otherwise every visitor will get an inexplicable error for the next 30 minutes.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // Let the cache expire and force it to go back to the source

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("The error should be passed transparently to the caller when it has canceled")
	}

	// Key invariant: The time it was canceled does not leave any trace - there is no "cancelled" error in the cache,
	// The good results from last time are still maintained.
	if c.err != nil {
		t.Fatalf("Cancel error should not be written to cache, got %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("The cache should keep the last good result, get %+v", c.rel)
	}

	// That cancellation didn't result in any new data, so the next visitor should go back to the source - and get the results normally,
	// You will not be affected by the last cancellation.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("Normal requests after cancellation should not report an error: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("Should get normal results, get %+v", rel)
	}
}
