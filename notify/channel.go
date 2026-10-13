package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel is an adapter for notification channels. The implementation must be **stateless**: the same instance will be used by multiple channels
// Configure concurrent reuse, and the credentials are always passed in from the cfg parameter.
type Channel interface {
	// Kind returns the channel type identifier, which must be consistent with the key in the registry.
	Kind() string
	// Validate is called when saving the configuration to verify required fields and formats. The returned error will be displayed directly to
	// Configurator, so the copy should explain "Which field is missing" rather than the general "Configuration is invalid".
	Validate(cfg map[string]any) error
	// Send delivers the message once and returns the number of items actually delivered and an error.
	//
	// Why should the number of items be returned: Each platform has a maximum message length limit, and the summary messages will be truncated when they cannot fit into the entire batch.
	// If the caller unconditionally marks the entire batch as delivered, the truncated entries disappear—not visible in the message.
	// The delivery history also shows success, and no vulnerability can be found anywhere that has never been sent. After returning kept,
	// The caller only marks the first kept bar and leaves the rest for the next batch.
	//
	// Returning an error indicates delivery failure, where *PermanentError indicates that retry should not be performed.
	// kept is meaningless on failure and should be ignored by the caller.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin returns the official recommended delivery limit per minute of the channel as a new channel instance
	// The default current limit value at the time. Returns 0 for no known limits.
	DefaultRatePerMin() int
	// SecretKeys returns the key names belonging to the credentials in the channel configuration. The values of these keys will be masked when echoed by the API.
	// If the mask value is received during update, the original value in the library will be retained. Only the implementation knows which fields count as credentials
	// (The entire Webhook address of Enterprise WeChat is the credential, and DingTalk is only the secret),
	// Therefore, this knowledge must be provided by channels and cannot be guessed by the upper management.
	SecretKeys() []string
	// DestinationKeys returns the key name in the channel configuration that determines "where the message is sent".
	//
	// Like SecretKeys, it is a security-related thing: the target address and credentials are two independent sets of fields.
	// If "only the address is changed and the credentials are retained" is allowed, anyone who can change the channel configuration can change the real credentials in the library.
	// If it is sent to a server that you control, the mask configured in the channel will be completely meaningless.
	// See PrepareConfigUpdate for details.
	DestinationKeys() []string
}

// registry is the channel registry. Deliberately use explicit literals instead of init() self-registration: so "what channels are there?"
// You can see it all in one place, and new channels will expose omissions at compile time rather than relying on runtime side effects.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get gets the channel implementation by type.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind reports whether kind is a supported channel type.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds returns all supported channel types, arranged in dictionary order (for stable display in UI drop-down).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError marks a delivery failure that should not be retried: incorrect credentials, target rejection, invalid request body, etc.
// Retry is only meaningful for transient failures (network jitter, current limiting, peer 5xx); for permanent failures, repeatedly back off and retry
// It will not succeed, and the real error will be flushed in the retry log.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks err as a permanent failure. Return nil when err is nil,
// It is convenient to write `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether the err chain is marked with a permanent failure.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- Configuration reading helper ----
//
// The channel configuration comes from the JSONB column of the database, which is map[string]any after encoding/json deserialization.
// Values are always float64, and arrays are []any. The following helpers unify this layer of transformations and tolerate users
// Type deviation caused by leaving blanks in the UI (such as filling in the port as a string).

// cfgString takes the string configuration item, and the leading and trailing blanks are cut off - it is easy to copy and paste from the web form.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt takes the integer configuration item and is compatible with float64 (JSON default) and string sources.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool takes the Boolean configuration item, compatible with the string "true"/"1".
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings takes the string array configuration item, automatically trims blanks and discards empty strings.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Also accepts a single string to facilitate form submission when there is only one value.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap takes string mapping configuration items (such as custom HTTP headers), trims all key values, and discards empty keys.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
