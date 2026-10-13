package notify

import (
	"fmt"
	"strings"
)

// This document is a message rendering shared by "Markdown system" channels (DingTalk, Enterprise WeChat).
// Feishu uses JSON for cards, HTML for Telegram, and HTML for emails, and each is rendered in the adapter.

// maxAssetsShown is the maximum number of assets listed in the message. A vulnerability may anchor dozens of assets.
// The full list will be too crowded and has no informational value - no one will look at domain names after the 4th one in IM.
const maxAssetsShown = 3

// maxSummaryRunes is how many characters the summary is compressed to. The IM message is "Prompt to see details",
// It is not the report itself, the complete content is in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes reserved for message headers (summary rows + level distribution + possible truncation hints)
// with tail (platform link). When packaging as a whole, deduct this part from the budget to ensure that the head and tail will not be cut off——
// Once the beginning and end are cut off, readers cannot even tell "which batch this is and how many are not displayed."
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why it is necessary: The vulnerability title, summary, type, and asset display name all come from **untrusted sources**——
// The title and summary come from the model output (the model reads the response of the target under test), and the asset url is the scanned
// The complete URL obtained (including target-controllable query string). Without escaping, a title would be
//
//	Login port SQL injection\n[Emergency: Click here to verify account](http://attacker.tld)
//
// The vulnerability will be rendered as a clickable external link in the security engineer's DingTalk/Feishu; and
// `![](http://attacker.tld/beacon)` will be pulled by the client during rendering, which is equivalent to notification
// "This vulnerability has been seen" and reveals the reader's IP. Even if it is harmless content, injected bold or
// Quote blocks can also squeeze critical holes underneath out of the fold.
//
// The escape set covers the categories of title/link/emphasis/list/quote/strikethrough, which will change the structure or make it clickable.
// The character of the element. `\` must be processed first, otherwise the trailing backslash will be escaped again.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText compresses untrusted text into a single line and escapes it for use by markdown body.
// Singlelining is the other half of escaping: the newline itself can fake a new list item or block of quotes.
// And escape characters can't stop it.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (title bar/card title of the IM platform), the content is **unescaped original text**.
//
// There is deliberately no escaping here: this title is shared by the renderers of four contexts - markdown text, Telegram's
// HTML, plain_text of Feishu cards, and JSON and email subject of general webhook. per context
// The escaping rules are different (markdown escaping when inserted into HTML will leave visible backslashes, and when inserted into JSON, it will pollute
// data), so escaping must be taken care of by the respective output end, see writeItem/feishuItemLines/
// telegramEscape. I once added markdown escaping in the shared function, and the result was in the Telegram message
// A visible backslash like `\(1\)` appears.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability summary · Total %d items", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body and returns the body and the actual number of entries written.
//
// The return value kept is the number of items actually delivered by this delivery. Accordingly, the caller only marks the first kept item as
// Delivered - Items blocked by the maximum channel length must be kept for the next batch, rather than being sent along with them.
// Marking successful. This is where "silent loss" comes in: the message is truncated, but the delivery record shows it was fully delivered,
// Nowhere does it appear that the second half was never issued.
//
// maxBytes<=0 means no limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single message will still be sent even if it is too long (it will be truncated in the end): partial information of a vulnerability
		// It's better than not sending one at all.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\\n[View all in platform](%s)\\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro renders the beginning of the summary message: time window, number of items and level distribution.
// With these, people who receive the summary can judge whether this batch needs to be processed immediately without clicking into the platform.
//
// items is the items actually loaded, and total is the total number of items in this batch. When the two are different, it must be stated clearly
// "How many more messages are there in the next message?" Otherwise, readers will think that the number written in the header of the message is all.
// The later items that have never been sent do not exist on the interface at all.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**In the last %d minutes, %d new vulnerabilities were added**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**Added %d vulnerabilities**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(Showing the first %d messages; the rest (%d) continue with the next message)", len(items), extra)
	}
	// The distribution is given by level, allowing readers to see at a glance whether there are serious items. Only count **this article actually contains**
	// Items, make sure "Severe 3" is consistent with the items that can be counted below.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem renders a single vulnerability entry.
//
// prefix is the serial number used to summarize the list; when single=true, the full version (including summary and backlink) is rendered.
// Only render one line of summary in summary list - otherwise 50 summaries will become one long document.
//
// All content from outside (title/type/assets/summary) goes through markdownText:
// Singleline + escape. The backlink is spelled out by the public_base_url configured by the administrator, and is not untrusted content.
// And it must be a clickable link, so it is output as is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Summary mode: Presented as a single line, with asset and summary compression following.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status change**: %s → %s\\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Type**: %s\\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Asset**: %s\\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Summary**: %s\\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[View details](%s)\\n", it.DetailURL)
	}
}
