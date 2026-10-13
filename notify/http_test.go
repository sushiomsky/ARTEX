package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// This document covers doJSON's HTTP layer error classification.
//
// Why test separately: Each channel adapter only cares about the platform’s own business error codes (DingTalk errcode,
// Feishu code, Telegram ok field), and the grading of **HTTP layer** is done uniformly by doJSON.
// The two are two independent lines of defense. Without this, a transit gateway that returns 503 will be regarded as a permanent failure.
// Just give up the retry; and a 403 will be regarded as retryable, and three rounds of retreat will be in vain.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 Success is not a mistake", 200, false},
		{"429 Current limit can be retried.", 429, false},
		{"408 Request timed out and can be retried", 408, false},
		{"500 server error can be retried", 500, false},
		{"502 gateway error can be retried", 502, false},
		{"503 Service unavailable, please try again", 503, false},
		{"400 parameter error permanent failure", 400, true},
		{"401 Authentication failed, permanent failure", 401, true},
		{"403 Forbidden access permanently failed", 403, true},
		{"404 Address does not exist. Permanent failure.", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx should not report error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("If it is not 2xx, an error should be reported.")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("HTTP %d permanent error: expected %v got %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// The status code must appear in the error, otherwise the user will not be able to determine whether he has made a misconfiguration or the other end has hung up.
			// Assert numbers instead of Go's English StatusText: The copy of this package is in Chinese
			// (Consistent with the rest of the project), numbers are the language-independent, assertion-stable part.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("The error message should have HTTP status code %d, get %v", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet overrides snippet: the error description returned by the peer should be brought back.
// Otherwise, the user only knows "failed" and does not know why the peer refused.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("An error should be reported")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("The error message should bring back instructions from the peer, getting %v", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded constrains the form of snippet:
// Its response to the client will be entered into the last_error column and the front-end table as it is. Multiple rows/excessive length will destroy the layout and load.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// Responses with line breaks, tabs, and very long content of 5000 characters.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("An error should be reported")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("The error message should be compressed to a single line, resulting in %q", msg)
	}
	// The snippet limit is 200 characters + fixed prefix, and the total size must be much smaller than the original response.
	if len(msg) > 400 {
		t.Errorf("Error message is too long (%d bytes) and should be truncated by snippet: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse confirms that there is an upper limit for reading: when the peer returns abnormally large content
// The entire response cannot be read into memory (a copy of last_error will be stored for each entry in the delivery history).
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("An error should be reported")
	}
	if len(err.Error()) > 400 {
		t.Errorf("Oversized response should be read with limited length and truncated, error message length %d", len(err.Error()))
	}
}
