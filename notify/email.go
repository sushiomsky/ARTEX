package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout respectively restrict the connection establishment and the entire SMTP session.
// net/smtp itself does not have any timeout mechanism. If these two are not provided, a stuck peer will cause
// The delivery goroutine is permanently hung there - and the dispatcher is processed serially by a single goroutine.
// It's equivalent to the entire notification system shutting down.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel implements SMTP email delivery.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// There is no platform limit for email, but you should not use it to flood the screen; give a loose default value.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Only the password is masked. The SMTP host, account, and recipients are not secrets, and masking them will only make editing troublesome.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port determines which server to hand over the password to; tls determines whether to encrypt the transmission. Any change in the three
// Requesting to re-state the password - Incidentally, the "turning off TLS" step must explicitly bring the credentials, rather than just changing them.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("Missing SMTP server address")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("Invalid SMTP port (should be 1-65535)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("Missing sender address")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("At least one recipient address is required")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: Upgrade if the peer supports it. Credentials cannot be issued in clear text sessions (see auth description below).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS failed: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth will refuse to send credentials over an unencrypted connection (unless the target is localhost).
			// This is the **correct** security behavior and cannot be bypassed, but the reason needs to be translated clearly——
			// Otherwise, users will only see "unencrypted connection" and not know what to do.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("Credentials refused: The connection is not encrypted. Please enable TLS, or use port 465 instead (implicit TLS), or check \"Enable TLS\" (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP authentication failed: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("Sender %s rejected", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("Recipient %s rejected", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("Failed to write email body: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("Failed to submit email: %w", err)
	}
	// Failure of Quit does not affect the fact that the message was received by the server, so the error is ignored.
	_ = client.Quit()
	// There is no length truncation of the email (the entire HTML body is sent), and the entire batch is considered delivered.
	return len(m.Items), nil
}

// emailDial establishes an SMTP connection.
//
// implicitTLS=true goes to 465 this kind of "connection is TLS" method; false goes to 25/587 after establishing the connection in plain text.
// STARTTLS. The two cannot be mixed: greeting messages sent to port 465 will be directly disconnected.
//
// The session duration is set at the **connection establishment** (instead of setting it up afterwards), because the client of net/smtp uses the underlying
// The connection is hidden in the unexported field and cannot be obtained externally; once the connection is handed over, it can only rely on the preset deadline.
// Get the bottom of it. This also covers blocking during the handshake phase.
// Control hangs blockInternalDial and HTTP-based channels share the same guard. If not, SMTP
// This is the gap in the entire SSRF protection: fill in the host as 169.254.169.254 or 127.0.0.1 and you can connect directly.
// When the smtp.NewClient handshake fails, the line returned by the peer will be included in the error, last_error
// It is echoed by the delivery history interface to form a semi-blind reading primitive; the time-consuming difference of "connection refused vs timeout" can also be
// Used to detect ports. The dial-up phase is the final point of effect and also covers DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to connect to SMTP server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP handshake failed: %w", err)
	}
	return client, nil
}

// smtpStageError divides the failure of a certain stage into "retryable" and "permanent failure" according to the SMTP response code.
//
// Why it is necessary to distinguish: SMTP’s 4xx and 5xx semantics are completely different——
//   - 4xx (450 Greylisting, 451 Local Error, 452 Insufficient Storage) are **temporary** rejections,
//     The normal approach is to try again later; especially for graylisting, you will encounter it almost every first time.
//   - 5xx (550 user does not exist, 553 address is illegal) is a permanent rejection, and retrying is meaningless.
//
// If it is a permanent failure, a mail server with greylisting enabled will make **every** push happen for the first time.
// Failed after trying - and this kind of failure is exactly the scenario where automatic retry should be most useful.
// The response code takes the first three digits of the error text; if the code cannot be obtained, it will be processed as retryable (I would rather try one more time,
// Don’t judge a possible instantaneous failure just because it cannot be analyzed).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode takes the leading three-digit response code from the SMTP error text. If it cannot be found, 0 is returned.
// net/smtp does not export the error code field, it can only be taken from the text; the format is "450 4.7.1...".
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage Assembles a complete RFC 5322 email.
//
// There are two reasons why the text is encoded in base64: First, SMTP stipulates that a single line should not exceed 1000 bytes, and HTML
// The body text (especially summary emails) is prone to very long lines; secondly, base64 naturally does not start with "."
// line, saving you the trouble of escaping SMTP dots.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// The Chinese theme must be RFC 2047 encoded, otherwise it will be displayed as garbled characters by the client.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// There is no hard limit on the length of the message, so the body is not truncated.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 wraps to 76 characters, compliant with RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
