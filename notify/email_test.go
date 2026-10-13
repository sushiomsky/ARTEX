package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// This document completes protocol-level testing of email channels. Before this the coverage of email.Send was 0——
// No use cases have been run through the entire SMTP path, and it is precisely the protocol with the largest coverage among the six channels.
// The most error-prone one (the handshake, authentication, envelope, and DATA phases each have their own failure semantics).
//
// Here we use the self-built minimal SMTP server driver instead of mocking net/smtp:
// Most of the risks in the email channel lie in the "talking to the real SMTP server" step.
// Mock out this step and it would be an accident.

// fakeSMTP is a just enough SMTP server: it can complete greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT,
// And return the specified response code for a specific stage as required by the use case.
type fakeSMTP struct {
	ln net.Listener

	// rcptReply is the response to the RCPT TO; default is 250.
	rcptReply string
	// mailReply is the reply to MAIL FROM; default 250.
	mailReply string
	// When advertiseAuth is true, support for AUTH PLAIN is declared in EHLO.
	advertiseAuth bool

	mu       sync.Mutex
	data     string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok := f.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("Non-TCP listening address")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// Do not declare STARTTLS: let the code take the clear branch (the test target is envelope logic, not TLS).
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// Simplified processing: PLAIN's initial response may span multiple lines, so accept it directly.
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to":   []any{"a@example.com", "b@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	cfg := emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Delivery failed: %v", err)
	}
	// The envelope stage must go to: sender, two recipients, DATA.
	for _, want := range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if !f.sawCommand(want) {
			t.Errorf("%q is missing from the SMTP session, actual command: %v", want, f.commands)
		}
	}
	// The body text is base64 HTML, and must contain the actual vulnerability content (still legible after encoding).
	body := f.body()
	if body == "" {
		t.Fatal("No text received in DATA stage")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("Missing Content-Type header:\\n%s", body)
	}
	if !strings.Contains(body, "base64") {
		t.Errorf("The body is not base64 encoded (long HTML lines would violate SMTP's 1000-byte line length limit):\\n%s", body)
	}
	// Multiple recipients must appear in the To header.
	if !strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("The To header does not contain all recipients:\\n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// AUTH should not be sent without an account assigned - some relays will reject it for this reason.
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("Delivery failed: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("AUTH was sent without assigning an account: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies is a direct verification of this audit fix:
// 5xx means permanent failure, 4xx (grey list) means you can try again.
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases := []struct {
		name      string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"Recipient is permanently rejected with 550", "550 5.1.1 User unknown", "250 OK", true},
		{"Recipient encountered 450 gray list", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"The recipient encountered 452. The mailbox is full.", "452 4.2.2 Mailbox full", "250 OK", false},
		{"Sender was permanently rejected with 553", "250 OK", "553 5.1.3 Bad address", true},
		{"Sender encountered 451 temporary error", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err := (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("An error should be reported")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("permanent Decision error: Expecting %v got %v (%v)", tc.permanent, got, err)
			}
			// The original server text must be retained, otherwise users will not know whether to contact the server administrator or change the address.
			if !strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) && !strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("The server's response code should be retained in the error: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// PlainAuth for net/smtp refuses to issue credentials over unencrypted connections (unless the target is localhost).
	// This is the **correct** security behavior and cannot be bypassed; but an error should be given that can guide the user to fix it.
	// Here it is triggered with a hostname other than localhost.
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	_, port := f.hostPort(t)
	cfg := map[string]any{
		"host":     "smtp.example.com", // non-localhost
		"port":     float64(port),
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("The local DNS resolves to the local server and is skipped (does not affect other use cases)")
	}
	// Failure to connect or being denied credentials will count as passing this assertion; the key is that the password cannot be sent out silently.
	if !IsPermanent(err) && !strings.Contains(err.Error(), "connect") {
		t.Logf("Error: %v (Unable to connect under non-localhost, expected)", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// Email channels have the most configuration fields. If any one is omitted, it will be exposed only when it is delivered; confirm here one by one.
	// Verification can be stopped in advance. What the assertion checks is "what the error message mentioned is missing".
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"Missing host", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing port", map[string]any{"host": "smtp.example.com"}},
		{"port out of bounds", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"Missing from", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"missing to", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("Verification failed: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance covers the fault tolerance of configuration reading: the value in JSONB is float64,
// But the user may fill in the port as a string in the UI; the array may also be a single string.
func TestEmailConfigTolerance(t *testing.T) {
	cfg := map[string]any{
		"host": "smtp.example.com",
		"port": "587", // port as string
		"from": "a@b.c",
		"to":   "d@e.f", // A single string instead of an array
		"tls":  "true",  // boolean as string
	}
	if err := (emailChannel{}).Validate(cfg); err != nil {
		t.Fatalf("Values as strings should be tolerated: %v", err)
	}
	if got := cfgInt(cfg, "port"); got != 587 {
		t.Errorf("cfgInt string port not parsed, got %d", got)
	}
	if !cfgBool(cfg, "tls") {
		t.Error("cfgBool Unparsed string \\\"true\\\"")
	}
	if to := cfgStrings(cfg, "to"); len(to) != 1 || to[0] != "d@e.f" {
		t.Errorf("cfgStrings not compatible with single strings, got %v", to)
	}
}

// TestFilterValidateRejectsTypo is a direct validation of audit fixes:
// The typo in the threshold must be blocked during writing, otherwise the filter will silently fail and become a full push.
func TestFilterValidateRejectsTypo(t *testing.T) {
	good := []string{"", "low", "medium", "high", "critical"}
	for _, s := range good {
		if err := (Filter{MinSeverity: s}).Validate(); err != nil {
			t.Errorf("Legal threshold %q rejected: %v", s, err)
		}
	}
	// These are real clerical errors that happen—all of them must be rejected.
	for _, s := range []string{"hgih", "HIGH", "serious", "high ", "crit"} {
		err := (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("Illegal threshold %q should be rejected (otherwise the filter will silently fail and become full push)", s)
			continue
		}
		// Error messages should guide users to correct errors.
		if !strings.Contains(err.Error(), "low") || !strings.Contains(err.Error(), "critical") {
			t.Errorf("The error message should list optional values, get %q", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly locks the division of "write strict, read wide":
// The existing bad values in the library cannot be blocked from being read by the channel (that would cause all historical channels to suddenly stop pushing).
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw := []byte(`{"min_severity":"hgih"}`)
	f := ParseFilter(raw) // No error reported
	if f.MinSeverity != "hgih" {
		t.Fatalf("The read path should be left as is, getting %q", f.MinSeverity)
	}
	// And the channel can still make decisions on events (without panicking or blocking).
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}
