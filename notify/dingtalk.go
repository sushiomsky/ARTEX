package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel implements DingTalk custom robot.
//
// Platform features (which determine the implementation choices here):
//   - The current limit for a single robot is 20 messages/minute, and excessive messages will be silently discarded (HTTP may still be 200).
//     Therefore, current limiting must be done on the client side, see DefaultRatePerMin.
//   - Choose one of three security settings: signature / custom keyword / IP whitelist. Signing is the only thing that does not depend on
//     Message content scheme, so only signatures are supported (naked webhooks that do not open any of the three are also supported).
//   - HTTP 200 is returned for success/failure, distinguished by the errcode in the body - errcode is not checked
//     Delivery failures will be recorded as successes.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// DingTalk's webhook address contains access_token, which itself is the credential, so it is masked overall.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// The target is the DingTalk webhook address itself; when changing the address, the new address must be re-signed and the signing key must be re-signed.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing webhook address")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Invalid webhook address: %w", err)
	}
	return nil
}

// Send delivers a message once. Use ActionCard (with buttons) when there is a backlink and it is a single link, otherwise use markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown text does not have a clear byte limit, but it still provides upper limit protection to avoid abnormal expansion of the evidence field.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "View details",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk hides business errors in 200 responses.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Failed to parse DingTalk response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 means signature verification failed, 310000 means keyword mismatch - all are configuration errors.
		// Retrying will not heal itself.
		return 0, Permanent(fmt.Errorf("DingTalk returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL adds timestamp and sign parameters to the webhook according to the official signing rules.
//
// Rule: String to be signed = timestamp + "\n" + secret, the **key of HMAC-SHA256 is also secret**,
// The result is base64 post-URL encoded. timestamp is milliseconds. If secret is empty, it is returned unchanged.
// To support robots that do not have signing enabled.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Non-transparent err: The error text of url.Parse contains the complete address (including access_token).
		return "", fmt.Errorf("Failed to resolve webhook address: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL verifies that the address is available, the protocol is supported, and makes intranet judgment on the literal IP target.
//
// Two points to note:
//
//  1. **Error messages must be desensitized**. url.Parse itself returns *url.Error, and its Error() has
//     **Complete original address**, and the addresses of these companies with this function have credentials embedded in them (DingTalk access_token,
//     Qiwei key, Telegram’s bot token, Feishu hook id). In the past, `return err` was used directly here.
//     So the error "Illegal address format" took the credentials out, and the 400 response flowed to the test interface.
//     Last_error, server log and delivery history interface for each delivery.
//
//  2. **The literal IP directly determines the internal network**, and the domain name is left to the dial-up stage (blockInternalDial is the final
//     Effective point, can also cover DNS rebinding). Do this once so that you can get a prompt when saving the configuration.
//     Instead of waiting until the first delivery fails.
//
// Restrictive protocols are defensive: file:///gopher:// and the like can cause unexpected behavior in http.Client
// Behavior (although it has been blocked by the scheme check, there is no reason to let this side go).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("The address cannot be resolved (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Only supports http/https, received %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("Missing hostname")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("Refuse to deliver to the local/link local address %s (if you really need to deliver to the local service, set %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
