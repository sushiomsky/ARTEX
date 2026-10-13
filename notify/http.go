package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets determines whether delivery of messages to loopback/link-local addresses is allowed.
//
// Denied by default. These addresses are not where IM robots or public mail servers will appear, but they can
// The things hit are very sensitive: the management port of another service on the same machine, and the metadata endpoint of the cloud environment.
// (169.254.169.254, reads instance credentials). The delivery address was assigned by the administrator, but one was XSS/CSRF
// A borrowed management session, or a second person sharing the same JWT, can read the response content back by changing the configuration.
// ——doJSON will write the first 200 bytes of the 4xx/5xx response body into last_error, and the delivery history interface
// will echo it out, which is a semi-blind reading primitive.
//
// But "native SMTP relay" (postfix on 127.0.0.1:25) is a common configuration for self-built mail.
// One size fits all will trap people. So leave an explicit escape hatch instead of hard-coded escape:
// Set ARTEX_NOTIFY_ALLOW_LOCAL=1 to allow this.
//
// Exported as AllowLocalTargetsEnv so that tests can open it explicitly - this package is the same as the server package
// The use case uses a large number of httptest fake receivers on 127.0.0.1. If they are not opened, they will all be blocked by guards.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP reports whether the target IP belongs to the "default delivery is not allowed" address range.
//
// Only deny loopback, link local (with cloud metadata 169.254.169.254), unspecified and multicast.
// **Do not reject** RFC1918 Private network: Self-built Mattermost/SMTP relay on the intranet is a very common legal usage.
// Blocking them all would make the functionality unavailable in a real environment. This trade-off is deliberate——
// Protection must block truly sensitive targets, and at the same time, normal deployment must not be abolished.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) must be restored to IPv4 before being judged, otherwise the check will be bypassed.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial is the Control hook of the http.Transport dialer, when the connection is established**
// Check the destination address.
//
// Why is it set in the dial-up stage instead of only verifying when saving the configuration: This is the final point of effect.
// It also covers two scenarios for bypassing configuration verification - DNS rebinding (resolving to public IP,
// Resolve to the intranet when actually connected) and redirect (although we have rejected cross-host jumps, but same-host jumps
// may still point the path elsewhere).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("Unable to resolve destination address %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("Refuse to deliver to the local/link local address %s (if you really need to deliver to the local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport only adds a dial guard based on the default Transport.
// Use Clone to retain all default tuning (connection pooling, HTTP/2, timeouts, proxy, etc.),
// Avoid changing other behavior just to add a check.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient is the client common to all channel delivery.
//
// Deliberately **not** reuse the project's global exit proxy (GlobalProxy on the server side): that proxy is for penetration
// Target traffic often uses unstable tunnels, and the availability of notifications should not be hijacked by jitter in the target network.
// Just push the IM to connect directly. The timeout is set to 15 seconds - a peer slower than this is effectively a failed state.
//
// Reject cross-host redirection: The delivery address of this function is in the form of "a fixed endpoint", which normally does not
// Redirect to other hosts; and the credentials of these companies (DingTalk’s access_token, Qiwei’s key, Telegram’s
// bot token) is in the URL, and following a cross-host jump is equivalent to handing the credentials to the redirect target. Same host
// Jumps (such as trailing slashes) are still allowed.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("Too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("Deny cross-host redirection (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit limits the size of the read response body. When the peer is abnormal, it may spit back very large content, but we only need
// The error code and a short error description are used to display in the delivery history.
const respBodyLimit = 8 << 10

// doJSON sends a request and returns the response body (limited length).
//
// When the payload is nil, an empty body is sent (used for GET or when the platform does not require a body).
// The key values ​​in headers are appended as-is for custom headers for universal webhooks.
//
// Error classification is the core responsibility of this function: network layer failures and 5xx/408/429 are classified as "retryable",
// The remaining 4xx are classified as "permanent failures" - retrying a 403 just flushes the log three times with the same error.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// The serialization failure is a local bug (the configuration field type is wrong), and retrying will not improve it.
			return nil, Permanent(fmt.Errorf("Failed to construct request body: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// The URL is illegal - most likely the user filled in the wrong address, which is a permanent failure.
		// It is also not possible to transparently transmit err: the error text of url.Parse contains the complete address.
		return nil, Permanent(fmt.Errorf("Illegal request address: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout - mostly transient failures, leave it to backoff and retry.
		//
		// The erroneous text must be desensitized before being disseminated externally. Reason: http.Client.Do returns *url.Error,
		// Its Error() is `Op "Full URL": underlying error`, and the credentials for this function are in the URL**
		// (DingTalk access_token, Qiwei key, Feishu hook id, Telegram /bot<token>/).
		// If not desensitized, the credentials will flow to four places along this error: notification_deliveries
		// last_error (clear text dropped into the database), response of the delivery history interface (**Bypass channel configuration mask**),
		// Server logs, and the 502 text returned by the test sending interface to the front end.
		return nil, fmt.Errorf("Request failed: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("Failed to read response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (current limiting) and 408 (timeout) are worth retrying; the remaining 4xx are configuration or permission issues, and retrying is meaningless.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("The other party's current limit or timeout (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("The other party's service is abnormal (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("Request rejected by the other party (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet compresses the response body into a short line of text for error messages. The response may contain line breaks and a lot of whitespace.
// Directly inserting last_error will cause the layout of the delivery history page to collapse.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget compresses the delivery address into "scheme://host/…" for error messages.
//
// This is the only address desensitization method in this package, and it is deliberately made **rough enough**: in addition to scheme and host,
// Discard the rest. The reason is that there is no "universal and safe" way to determine which part of the URL is the credential:
//
//	DingTalk credentials are in query /robot/send?access_token=xxx
//	Qiwei credentials are in query /cgi-bin/webhook/send?key=xxx
//	Feishu credentials are at the end of the path** /open-apis/bot/v2/hook/<hook_id>
//	Telegram credentials are in the middle of the path ** /bot<token>/sendMessage
//
// If you want to "keep only the useful parts", you have to patch by channel, and missing any one is a credential leak.
// Keeping the host is enough for troubleshooting (it can be located if the DNS cannot be resolved, cannot be connected, or the certificate is incorrect).
// The specific robot is identified by the mask tail number prompt in the channel configuration.
//
// A fixed placeholder is returned when parsing fails - the original string is never echoed back.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(Address cannot be resolved)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError strips the address from the transport layer error, leaving only the underlying cause.
//
// The structure of *url.Error is {Op, URL, Err}, and Error() will type out the URL together.
// Here, the Err field is explicitly taken and its Error() is bypassed - more reliable than doing string replacement afterwards.
// Because replacement must correctly handle various variations of URL encoding/escaping, it is easy to miss.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: unknown error", uerr.Op, host)
	}
	// Non-*url.Errors (such as errors returned by redirection strategies) may also contain addresses, which are all desensitized.
	return redactURLsInText(err.Error())
}

// redactURLsInText replaces the http(s) addresses appearing in a piece of text into desensitized form.
//
// Used to catch errors that cannot get structured fields (redirect strategy errors, custom errors in third-party libraries).
// Only http/https prefixes are recognized, separated by whitespace and quotation marks - the address will not contain these two types of characters.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
