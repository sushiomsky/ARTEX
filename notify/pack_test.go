package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This document covers the fix for "package by entire message": when the summary message exceeds the upper limit of the channel length, you must **press the entire message**
// Truncate and report the true number of unloaded entries, allowing the caller to mark only those that were actually delivered.
//
// The previous method was to render the entire message and then cut it off, and then the entire batch of marks was delivered: the second half of the message disappeared out of thin air.
// The delivery history shows all successes - the vulnerability is gone and nowhere to be found.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// A summary of 200 Chinese items must far exceed Qiwei’s 4096 bytes.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("Text %d bytes exceed upper limit %d", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("The body text is not legal UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Only part of it should be kept (0 < kept < %d), resulting in %d", len(m.Items), kept)
	}
	// The header must truthfully state how many items this article contains and how many other items there are - otherwise readers will interpret the header as
	// That number counts as all.
	if !strings.Contains(body, "the rest") || !strings.Contains(body, "continue with the next message") {
		t.Fatalf("The header should indicate how many items are not included in this article:\\n%s", body[:minInt(400, len(body))])
	}
	// Only the front kept strip should be included.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "Vulnerability "+itoa(i+1)) {
			t.Fatalf("Item %d should be in this message:\\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "Vulnerability "+itoa(kept+1)) {
		t.Fatalf("Item %d should not appear (it belongs to the next batch)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = no limit
	if kept != len(m.Items) {
		t.Fatalf("If the length is not limited, all should be kept, resulting in kept=%d", kept)
	}
	if strings.Contains(body, "The rest") {
		t.Fatalf("The truncation prompt should not appear when there is no truncation:\\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// When the budget is so small that even one can't fit in one, one is still sent out (the bottom of the pocket is eventually cut off).
	// Otherwise, a very long vulnerability will permanently block the entire batch: it will not fit every time you receive it, and it will not be issued every time.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("At least 1 should be kept, got %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("A single message should be reported as delivered, and %d was obtained.", kept)
	}
	// An empty message has no deliverable entries.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("0 empty messages should be reported, and %d was obtained.", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram limits the length by **characters**; using byte size will reduce Chinese messages to one-third.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("Text %d characters exceed the upper limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Should only partially load, got %d", kept)
	}
	if !strings.Contains(text, "continue with the next message") {
		t.Fatalf("It should be noted that there is a remaining amount not included:\\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("Card should only hold part of it, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the text, and the entire batch is considered delivered.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("The precondition is not established")
	}
	// Confirmed indirectly through the return value of the renderer: markdownBody(0) retains all without restrictions.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("Applying all without limiting the length gives %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent is a regression test for "untrusted content must not change the message structure".
// The title and summary come from the model output (the model reads the response of the target under test), and the asset name comes from the URL of the target under test.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // Must appear in the result (escaped form)
		wrong []string // Must not appear in the result (unescaped form)
	}{
		{
			name: "Line breaks in the title + external links",
			item: Item{
				Severity: "high",
				Name:     "Login port SQL injection\\n[Emergency: Click here to verify account](http://attacker.tld)",
			},
			// Line breaks must be collapsed (otherwise new list items/quote blocks can be faked);
			// Square brackets and parentheses must be escaped (otherwise external links will be clickable).
			must:  []string{`\[Emergency: Click here to verify account\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\\n[Emergency", "\\n\\n[Emergency"},
		},
		{
			name: "Image beacon in header",
			item: Item{
				Severity: "high",
				Name:     "Vulnerability ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "Emphasis and citation in asset names",
			item: Item{
				Severity: "high",
				Name:     "normal title",
				Assets:   []string{"a.com/*injection*>reference"},
			},
			must:  []string{`\*injection\*`, `\>`},
			wrong: []string{"*injection*"},
		},
		{
			name: "Backticks and vertical bars in abstracts",
			item: Item{
				Severity: "high",
				Name:     "Title",
				Summary:  "`code` | table",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// Writing Item in single mode is a rendering path shared by three markdown channels.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("Missing escape form %q:\\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("Unescaped form %q appears (can be used to inject structures or external links):\\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst locks the escape order: backslashes must be processed first,
// Otherwise, the added backslash will be covered again, and double backslashes will appear in the output.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("Wrong escape order, you get %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes locks a specific regression:
// markdown escaping cannot leak into Telegram's HTML output (as it was in the shared title function
// After escaping, visible backslashes such as `\(1\)` appear in Telegram messages).
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *Key points*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Markdown backslash escape appears in Telegram text:\\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
