// Package notify implements the IM/email push channel adaptation layer for vulnerability discovery.
//
// Layering: This package is a **leaf package** and only relies on the standard library. It doesn't know the database or the server. Channel configuration
// Pass in map[string]any (corresponding to the JSONB column of notification_channels.config),
// The content to be pushed is passed in as Message. The advantages of this separation are: signature calculation, UTF-8 truncation, filtering and matching, etc.
// The truly error-prone areas can be separated from PostgreSQL single testing, and the host only needs to perform orchestration on the server side.
//
// Concurrency Contract: The implementation of Channel must be **stateless**. The same Channel instance will be configured by multiple channels
// (Even multiple robot instances of the same channel) Concurrent reuse, all credentials are passed in from the cfg parameter,
// Caching things like webhook URLs into fields that implement themselves is not allowed.
package notify

// Channel type identifier. The value is also a legal set of notification_channels.kind, which is determined by the server side.
// Whitelist verification (same as findings.status, no need for DB CHECK, convenient for adding channels later).
const (
	KindDingTalk = "dingtalk" // DingTalk custom robot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom robot
	KindWeCom    = "wecom"    // Enterprise WeChat group robot
	KindWebhook  = "webhook"  // Universal Webhook: Custom methods/headers/JSON templates
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP mail
)

// Event type, corresponding to notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the bottom value of kind that is empty in config.
const InitKind = KindDingTalk

// severityRank maps vulnerability levels into comparable ordinal numbers. Unknown level returns 0, so any
// The min_severity setting will keep unknown levels out - do not push when in doubt to avoid false positives and screen swiping.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the ordinal number of the level; 0 is returned for unknown levels.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel returns the Chinese level name with emoji, which is used for message titles and card colors.
// The unknown level is echoed as it is, not made up.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Critical"
	case "high":
		return "🟠 High"
	case "medium":
		return "🟡 Medium"
	case "low":
		return "🔵 Low"
	default:
		return severity
	}
}

// StatusLabel translates the disposition status into Chinese and is used for status change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "In Progress"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Resolved"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "False positive"
	case "ignored":
		return "Ignored"
	case "duplicate":
		return "Duplicate"
	case "risk_accepted":
		return "Risk accepted"
	default:
		return status
	}
}

// AtLeast determines whether severity reaches the min threshold. If min is empty, it means there is no threshold and all will pass.
// Note that unknown severity has an ordinal of 0 and will be rejected by any non-empty min (see severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
