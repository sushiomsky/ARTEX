package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// This document covers two related reinforcements:
//   ① The delivery address must not use the server as a springboard to enter the intranet/cloud metadata (SSRF)
//   ② The error message in address verification must not bring out the credentials in the address.
//
// About the test environment: A large number of use cases of this package use the httptest fake receiving end on 127.0.0.1, and the guard will block it by default.
// them. Therefore, AllowLocalTargetsEnv is turned on uniformly in TestMain, and each SSRF use case below
// Clear it explicitly to assert **deny by default** behavior.

func TestMain(m *testing.M) {
	// Allow regular use cases to connect to local fake receivers; SSRF use cases will temporarily clear themselves.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault is the core assertion of SSRF protection:
// By default, deliveries to the loopback address must be rejected by the connection layer.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // Close escape hatch = default behavior
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("Delivery to loopback addresses should not be allowed by default")
	}
	if hit {
		t.Fatal("The request has been sent to the local service - the guard is not effective")
	}
	// The error message should be able to guide the user on how to release it (the local SMTP relay is a legal configuration).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("The rejection message should explain how to explicitly release: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn reverse use case: it must be available after being opened explicitly.
// Otherwise, legal deployments such as local postfix/intranet relay will be abolished across the board.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("Should be deliverable after explicit release: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // Cloud metadata endpoint - the main reason for the existence of this function
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // The IPv4-mapped form must be restored before judging, otherwise the port will be bypassed.
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be rejected", s)
		}
	}
	// RFC1918 Private Network ** Deliberately Allowed **: Self-built Mattermost/SMTP relay on the intranet is a common and legal usage.
	// This assertion fixes this trade-off - if someone conveniently adds private network judgment in the future, this will fail.
	// This forces a conscious decision (rather than silently scrapping a batch of deployments).
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed (private networks are common legal delivery targets)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets overrides the pre-hint of the configuration phase:
// Literal IPs should be rejected when they are saved, rather than waiting for the first delivery to fail.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s should be rejected during configuration phase", raw)
		}
	}
	// The public network address and the private network address pass as usual (the private network is reserved for the dial-up stage and is not blocked there).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s should pass verification: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials is the audit pointing out branches that I missed in the last round.
//
// When url.Parse **fails**, *url.Error will be returned, and its Error() contains the complete original address. Last round I only
// The return error of http.Client.Do was desensitized and missed here; and the "permanent failure path" use case was added at that time
// (file://, gopher://, ftp://) can actually be successfully parsed by url.Parse, using the scheme branch.
// So being completely green doesn’t prove that this path is safe – it’s a false guarantee.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // Illegal percent sign escape
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // port non-numeric
		"http://[::1?access_token=" + leakProbeToken,                  // brackets not matching
	}
	for _, raw := range cases {
		// First confirm that this input is **certain** so that url.Parse fails. If you don't do this step, the use case may be
		// Walk to another branch without realizing it (this is how the false guarantee in the last round came about).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q should have failed to parse, otherwise this use case did not cover the target branch", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q should verify failed", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// Confirm that the packaging at the channel level does not take the address out.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("Illegal address should fail verification")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault Overrides the dial guard for the SMTP channel.
//
// The email channel used to use bare net.Dialer, which was the only gap in the entire SSRF protection: the host was filled in
// 169.254.169.254 or 127.0.0.1 can be connected directly, but when the smtp.NewClient handshake fails, it will be
// The row returned by the peer contains an error and is echoed by the delivery history interface through last_error - exactly the other channel
// The semi-blind reading primitive has been turned off; the time difference of "connection refused vs timeout" can also be used to detect ports.
//
// TestMain of this package has AllowLocalTargetsEnv turned on globally (a large number of use cases use 127.0.0.1 on
// Fake receiver), so this use case must clear it by itself - otherwise it will pass regardless of whether the guard is present,
// This is why the gap was not discovered by any tests in the first place.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // Close escape hatch = default behavior
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("Mail delivery to loopback addresses should not be allowed by default")
	}
	// The connection shouldn't be established at all: the guard blocks it in the Control hook and the EHLO is never sent out.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP session has been established - guard is not in effect")
	}
	// The error message should be able to guide the user on how to release it (the local postfix relay is a legal configuration).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("The rejection message should explain how to explicitly release: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn is the reverse use case of pairing: after explicitly opening
// Must be able to be delivered normally. Self-built SMTP/local relay on the intranet is a very common deployment, and guards cannot be one-size-fits-all.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Native SMTP should be deliverable after explicit release: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("No EHLO seen - session not actually established")
	}
}
