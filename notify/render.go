package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to no more than max bytes, ensuring that the result is legal UTF-8 and does not cut off characters.
//
// Why must it be cut according to character boundaries: Qiweiqun Robot’s markdown has a hard upper limit of 4096 **bytes** (not
// number of characters), while a Chinese character is 3 bytes. Directly slicing by bytes will cut a Chinese character in half, and the output is illegal.
// UTF-8 - The platform side either rejects the entire line or displays it as a garbled block. The approach here is to start with the budget position
// Go back to the latest rune start byte (utf8.RuneStart determines the continuation byte 0b10xxxxxx).
//
// max<=0 means no limit. Append ellipses after truncation, unless max is too small to fit.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis: abandon the ellipsis and simply truncate to avoid the result exceeding max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine compresses multiple lines of text into a single line: collapses all whitespace and truncates to the number of characters.
// Title line for IM messages - there are often line breaks in summaries, and inserting them directly into tables/titles will break the layout.
// max<=0 means no length limit.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to no more than max characters (not bytes), appending ellipsis if exceeded.
// max<=0 means no limit.
//
// The difference from TruncateBytes lies in the platform caliber: Qiwei has a length limit based on bytes, while Telegram has a length limit based on the number of characters.
// Using the wrong caliber will not report an error, but will only cause the message to be cut into a length much shorter than expected (Chinese 1 word = 3 bytes,
// Cutting 4096 by bytes only leaves about 1365 words), so both functions must be retained and selected according to channels.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML truncates HTML fragments according to the number of characters and ensures that no half-cut tags are generated.
//
// Directly truncating characters in HTML will cut out incomplete tags such as `<a href="htt`, and the platform parser will either
// If an error is reported, the entire article will be rejected, or the subsequent text will be swallowed as an attribute value. The approach here is: first truncate by characters,
// Check again to see if there is an unclosed `<` at the end, and if so, retreat to it.
//
// No tag balancing (completion </b> and the like): Telegram’s HTML parser will automatically close unclosed tags.
// To implement balancing yourself, you have to deal with quotation marks, comments, and self-closing tags in attributes, and the complexity is out of proportion to the benefits.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail is a fragment starting with `<` (there is no `>` after the last `<`), return it before `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a cut off HTML entity (for example, `&` is cut into `&amp`), it must also be returned.
	// Entity fragmentation in an entity-only parser may cause the entire message to be rejected - a message that exceeds
	// Aggregated messages with a maximum length are already common enough that it's not worth throwing away the entire notification.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount calculates how many items can be placed **completely** within the budget for the summary message to be packed as a whole item.
//
// Why click the whole item instead of rendering the entire item and then truncating it: truncation will make the second half of the item disappear out of thin air.
// And their delivery records will still be marked as delivered - it can't be seen in the message or in the delivery history.
// The vulnerability is gone. After packing the entire item, the items that cannot fit will remain in the library and become the next batch.
// The kept obtained by the caller is the number of messages actually delivered.
//
// Parameters: maxSize<=0 means no limit; reserve is the amount reserved for the header/tail of the message;
// size is responsible for measurement (the caliber of each platform is different: Qiwei/DingTalk by bytes, Telegram by the number of characters——
// Using the wrong caliber will not report an error, it will only suppress Chinese messages to a level far below the upper limit);
// render renders the idx item into its actual text - the length will vary depending on the content and cannot be estimated.
//
// Return at least 1 (as long as there are still entries). This message must also be sent when a single message is extremely long, and the caller's
// Finally cut off the bottom of the pocket, otherwise one very long vulnerability would stall the entire batch forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize are the two measurement calibers of packItemCount. They are named to avoid calling
// A naked func(s string) int closure appears, otherwise it would be difficult to tell at a glance which caliber is used.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders the asset list into a line of display text. If it exceeds limit, the rest will be omitted and the total number will be marked.
// A vulnerability may be anchored to dozens of assets, and listing them all would flood the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + ", and " + itoa(len(assets)) + " in total"
}

// itoa is a short alias of strconv.Itoa, which is only used for splicing display text to avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
