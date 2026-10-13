package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got := MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("Mask value reveals full credentials: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("The mask value should not expose the address body: %q", got)
	}
	// The last 6 digits should be reserved so that users can identify which robot it is.
	if !strings.HasSuffix(got, "123456") {
		t.Fatalf("The last 6 digits should be retained as identification hints: %q", got)
	}
	if !IsMasked(got) {
		t.Fatalf("The mask value must be recognized by IsMasked: %q", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// If the last 6 digits of a short credential are also exposed, it is equivalent to exposing the entire credential.
	for _, s := range []string{"abc", "abcdef", ""} {
		got := MaskedValue(s)
		if got != MaskedPrefix {
			t.Fatalf("Credentials of length %d should not give trailing hints, got %q", len(s), got)
		}
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("The mask value contains the original value: %q", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg := map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret":  "SECtest123456",
		"port":    float64(587),
		"host":    "smtp.example.com",
	}
	masked := MaskConfig(KindDingTalk, cfg)
	for _, k := range []string{"webhook", "secret"} {
		s, _ := masked[k].(string)
		if !IsMasked(s) {
			t.Errorf("%s should be masked, got %q", k, s)
		}
	}
	// Non-credential fields must be left intact, otherwise the UI cannot be displayed.
	if masked["port"] != float64(587) {
		t.Errorf("The non-credential field port should not be changed: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// When the channel type cannot be recognized, it is better to have the UI display an empty configuration than to spit back the original content that may contain credentials.
	got := MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got) != 0 {
		t.Fatalf("Unknown channel type should return empty configuration, got %v", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// The mask is a display layer behavior and cannot in turn change the true value in the library.
	cfg := map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig has modified the input parameters, which will cause the real credentials to be overwritten by the mask value.")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// The user only changed the method, and the browser submitted the mask value + new method.
	incoming := map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret":  MaskedValue("REALSECRET"),
		"method":  "PUT",
	}
	got := MergeConfig(stored, incoming)
	if got["webhook"] != "https://real/hook" || got["secret"] != "REALSECRET" {
		t.Fatalf("The mask field should retain the original value in the library, resulting in %v", got)
	}
	if got["method"] != "PUT" {
		t.Fatalf("The modified fields should take effect, get %v", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got := MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok := got["secret"]; ok {
		t.Fatalf("An empty string should clear the field and get %v", got)
	}
	// Fields not mentioned are retained (local update semantics).
	if got["webhook"] != "https://real/hook" {
		t.Fatalf("Fields not mentioned should be retained, getting %v", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got := MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"] != "smtp.example.com" || got["password"] != "pw" {
		t.Fatalf("Fields not mentioned should be retained, getting %v", got)
	}
	if got["port"] != float64(465) {
		t.Fatalf("The mentioned fields should be updated, got %v", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap is the most important security invariant of this package:
// **You cannot bring old credentials with you when changing the target address**.
//
// These use cases use exactly the same attack-shaped input (only changing the address, but not talking about the credentials).
// Rather than "correct input of defense logic" - if you only test the latter, the defense will still be all green even if the defense does not take effect.
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
		// wantMissing is the credential key that is expected to be named.
		wantMissing string
	}{
		{
			name: "General Webhook changes the address and wants to continue to use the Authorization header.",
			kind: KindWebhook,
			stored: map[string]any{
				"url":     "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming:    map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name:        "Telegram changes base_url and wants to send Bot Token to its own endpoint",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name:        "Email to change SMTP host wants to hand over password",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name:        "Even if TLS is turned off for email, the password must be re-stated.",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// Mask value = "Inherit old credentials", which must also be rejected in the context of an address change.
			name:        "Passback mask credentials + new address",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name:        "DingTalk changes the webhook and wants to keep the signing key",
			kind:        KindDingTalk,
			stored:      map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming:    map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("Changing the address without reasserting the credentials should be rejected; getting configured %v", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if !errors.As(err, &target) {
				t.Fatalf("A special error type should be returned so that the interface can give actionable prompts, resulting in %T: %v", err, err)
			}
			found := false
			for _, m := range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if !found {
				t.Fatalf("Should named missing credential key %q, got %v", tc.wantMissing, target.Missing)
			}
			// The error message should be able to guide the operator how to fix it.
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("The error message should mention %q: %v", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits reverse use case: normal editing cannot be blocked by mistake.
// Otherwise, this protection will be bypassed or deleted because it is "too annoying".
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
	}{
		{
			name:     "Only change the name (the configuration is returned as is)",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name:     "Only the request method is changed, the address and credentials are not changed.",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name:     "Change the address and give new credentials at the same time",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name:     "Change the address and explicitly state that the credentials are no longer required",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name:     "Telegram change chat_id (not destination)",
			kind:     KindTelegram,
			stored:   map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name:     "Change the recipient of the email (not the destination)",
			kind:     KindEmail,
			stored:   map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err != nil {
				t.Fatalf("Legitimate editor blocked by mistake: %v", err)
			}
			if merged == nil {
				t.Fatal("The merged results should be returned")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance covers a detail that is easy to misjudge:
// The port submitted by the front end is JSON number (float64), and the library reads it back as float64.
// But the two values may be of different types (like int vs float64). Comparing with == will judge "not changed" as "changed".
// As a result, "Please refill your password" will pop up for users who have only changed their names - false alarms will make people no longer trust this protection.
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored := map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// The same port, submitted as int.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err != nil {
		t.Fatalf("Identical port values (only different types) should not be considered an address change: %v", err)
	}
	// If the port is really changed, it must be blocked.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("Port changes should be blocked")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination overrides "can be left blank
// "Destination field" this path: Telegram's base_url is left blank to use the official API address.
//
// This used to make channels permanently fail to save from the second save:
//
//	When creating a new one, base_url: "" is saved in the library (the created path directly saves the config submitted by the front end without using MergeConfig)
//	→ When saving for the first time, MergeConfig treats the empty string as an explicit clear and deletes the key.
//	→ When saving for the second time, incoming is still "", but there is no such key in stored, so it is judged as "the address has changed"
//	→ bot_token is the mask echo value → 400 "The target address has changed, please refill the credential field at the same time"
//
// The user has not changed anything, but can no longer save the Bot Token unless it is re-pasted.
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored := map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// The front-end buildConfig() submits a value for each field definition of this channel: the credential backfill mask,
	// An empty text box submits an empty string. Here's a complete reproduction of its output, rather than just submitting the "changed keys".
	submit := func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id":   "-100",
			"base_url":  "",
		}
	}

	// First save: only the channel name is changed, and the config is returned as it is.
	merged, err := PrepareConfigUpdate(KindTelegram, stored, submit())
	if err != nil {
		t.Fatalf("The first save was blocked by mistake: %v", err)
	}
	if _, ok := merged["base_url"]; ok {
		t.Fatal("The premise has changed: the empty string should be deleted by MergeConfig - what this use case wants to cover is the step \"after the key disappears\"")
	}

	// Second save: The submitted content is exactly the same as last time, and the user has not changed anything.
	merged2, err := PrepareConfigUpdate(KindTelegram, merged, submit())
	if err != nil {
		t.Fatalf("The second save was blocked by mistake (the user did not change anything): %v", err)
	}
	// The third time, it was confirmed that it was not "only wrong once" but stable and saveable.
	if _, err := PrepareConfigUpdate(KindTelegram, merged2, submit()); err != nil {
		t.Fatalf("The third save was blocked by mistake: %v", err)
	}
	// The credentials must be preserved all the way and not cleared by empty string logic.
	if got := merged2["bot_token"]; got != "123:ABC" {
		t.Fatalf("Bot Token should use the original value and get %v", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges is the pairing of the previous use case
// Assertion: Treat the empty string as equivalent to "key does not exist", and you cannot let go of the real address change.
// These two directions are the real credential outgoing paths - Telegram's Bot Token walks in the URL path,
// Changing the base_url is equivalent to sending the Token to the new address.
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// Direction 1: Change from "empty" (official address) to a self-created address.
	official := map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err := PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("Changing from the official address to a self-created address requires refilling the Token.")
	}

	// Direction 2: Clear the self-created address (= switch back to the official API), which is also an address change.
	proxied := map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err := PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "",
	}); err == nil {
		t.Fatal("Clearing the self-created address (switching back to the official API) is also an address change and requires refilling the Token.")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	// Same as SecretKeys: if the channel forgets to declare the destination key, PrepareConfigUpdate cannot protect it.
	for kind, ch := range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("Channel %s does not declare a destination key, and the protection of changing the address to bring out credentials is invalid for it.", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("Channel %s did not declare a credential key", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// The compiler has forced each channel to implement SecretKeys. Here is another confirmation: "No channel is on the mask."
	// "Hand in blank" - a channel that returns an empty slice means that its credentials will be echoed to the browser in the clear.
	expect := map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch := range registry {
		if !expect[kind] {
			t.Errorf("Channel %s did not register mask expectations in test", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("Channel %s does not declare any credential fields and its configuration will be echoed in clear text", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer covers a hole pointed out by the audit:
// When stuffing masked sentinels into a **non-string** structure (e.g. webhook.headers is an object),
// MergeConfig only recognizes "string with prefix" as a mask, so the literal "__masked__" will be regarded as
// The real header value is stored in the database - subsequent authentication fails silently without any error.
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored := map[string]any{
		"url":     "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// Object internal entrainment mask sentinel.
	incoming := map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("Masked sentinels inside structures should be rejected (otherwise the literal will be stored in the library)")
	}
	// The overall commit object (the actual new value) is accepted as usual.
	ok := map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, ok); err != nil {
		t.Fatalf("Normal submission of new request headers should not be blocked: %v", err)
	}
}
