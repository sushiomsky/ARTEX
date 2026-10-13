package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard upper limit (bytes, non-characters) of markdown content of Qiweiqun robot.
// This is the tightest restriction of all six channels and the main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements enterprise WeChat group robots.
//
// Platform features:
//   - Only authenticates through the key on the URL, does not support signing - so the webhook address itself is the entire credential.
//   - The upper limit of markdown content is 4096 **bytes**. If it is too long, the entire article will be rejected (not truncated). Chinese 3 bytes/word,
//     This means that there are only more than a thousand words to write in the text and must be truncated by the client.
//   - The current limit is 20 items/minute, which is also controlled by the client's current limit.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// Enterprise WeChat only has one credential for Webhook (the key on the URL), and it does not support signing——
// The entire address is the entire credential, and no other fields need to be masked.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// Qiwei only has one field, Webhook, which is both the destination and the credential, so there is no "remaining credentials after changing the address" to speak of.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Missing webhook address")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Invalid webhook address: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// Summary batches can be very long (50 entries × one line per entry + prefix), and 4096 bytes can easily be exceeded.
	// The truncation is done here instead of relying on the platform to report errors: rejection means that the entire batch is lost, and truncation means at least the first few items are delivered.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Failed to parse Enterprise WeChat response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means that the interface call exceeds the limit - the current limit window of the platform will scroll, and retrying after backing off is effective.
		// So it is explicitly classified as retryable. Coming here shows that the client's rate_per_min is too aggressive.
		// Retrying is just a cover-up. The real fix is to lower the channel's current limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("Enterprise WeChat traffic limit %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means the webhook key is invalid - permanent failure and retry will not heal itself.
		return 0, Permanent(fmt.Errorf("Enterprise WeChat returns error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
