package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is a contract for the JSONB column notification_channels.filter: the filter condition for the channel instance.
// All fields are optional, and the default is "no filtering" - this is the hidden semantics of malformed configuration, see ParseFilter.
type Filter struct {
	// MinSeverity is the lowest level threshold (low/medium/high/critical), empty = no threshold.
	MinSeverity string `json:"min_severity"`
	// If TaskIDs / AssetIDs is an empty array, it means there is no limit; if it is not empty, it requires the event to intersect with it.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// If VulnClassInclude is empty, it means all are included; if it is not empty, it requires vulnclass to hit any of the keywords.
	// VulnClassExclude will be excluded if any keyword is hit (exclusion takes precedence over inclusion).
	// The matching method is case-insensitive substrings - safer than regular expressions: users mismatching regular expressions will not cause the channel to fail silently.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange determines whether this channel receives vulnerability status change events (only realtime mode is meaningful).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses channel filtering configuration.
//
// **Never return error. ** This is a deliberate design choice: filter conditions will always degenerate to zero when configured incorrectly.
// Filter (= no filtering = all hits), because for a vulnerability notification system, it is far better to push one more
// Silently missed a high-risk secret. Turning the parsing failure into "no push" is equivalent to giving the user a message that looks ready.
// Channels that actually push nothing – This is the worst failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// f remains zero when parsing fails, i.e. no filtering is performed.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a legal level threshold (an empty string indicates no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate verifies the **value-limited** fields in the filtering configuration and is called when saving the channel.
//
// Why must it be blocked when writing: Match’s judgment on the unknown threshold is `rank >= 0`, which is always true——
// In other words, if min_severity is a typo ("hgih"), the filter will **silently fail** and become
// "Push all". This is consistent with the direction of this package of "I would rather push more than miss out" (no leakage),
// But the consequence is that users think they are doing hierarchical push, but actually pour all vulnerabilities into the group.
// And there was nothing to suggest he was mismatched. This type of "silent degradation" should be stopped at the entrance.
//
// Note that Validate is only used for writing paths. The reading path still follows the permissive semantics of ParseFilter.
// In this way, the bad values ​​that already exist in the historical data will not make the channel unable to read.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("The lowest level %q is invalid, optional: low / medium / high / critical, or leave blank to indicate no limit", f.MinSeverity)
	}
	return nil
}

// Match determines whether an event should be delivered to the channel with this filter condition.
//
// **Never return error**, the reason is the same as ParseFilter: any internal exception is treated as a "hit".
// Determination sequence: event type → level threshold → task/asset scope → vulnerability type keyword.
func Match(f Filter, s Snapshot) bool {
	// Only channels that are explicitly opened will receive status change events. Off by default because most users
	// It is expected that "push" refers to "discovering new vulnerabilities" rather than following up on each status flow in a running account.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion priority: If any exclusion keyword is hit, you will be eliminated, even if the inclusion list is also hit.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A small set can be linearly scanned; the magnitudes on both sides are "dozens of human-made checks".
	// The cost of building a map outweighs the benefits.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of keywords (case insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
