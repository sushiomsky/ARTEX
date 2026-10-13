package notify

// Snapshot is a contract for the notification_events.snapshot JSONB column. The writing side is the db layer
// For vulnerability logging transactions, the reader is the delivery engine and filter matching of the server layer. The definition is placed in this package because it is
// "Notification field" payload: db is only responsible for serialization and does not understand the meaning of the fields.
//
// Why the vulnerability field is stored redundantly and not checked back during rendering: the vulnerability will be renamed, leveled, and status changed afterwards.
// The push content should reflect the conclusion of the event at the time of the incident - a review will result in "it was changed to low afterwards"
// Dangerously misleading. In addition, fan-out and rendering therefore do not need to JOIN the three tables of findings/tasks/assets.
type Snapshot struct {
	// Event type: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Only non-null when kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is a vulnerability to be pushed for the channel to render.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets is the resolved asset display name (such as domain name/IP). Filled by the server layer——
	// This package does not touch the database and cannot get the name.
	Assets []string
	// DetailURL is the link back to the vulnerability details; if it is empty, it means that public_base_url is not configured and is omitted when rendering.
	DetailURL string
	// Special for state change events; when both items are non-null, it is rendered as "Pending → Fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether the entry is a status change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the display title of the entry: giving priority to manually named name, fallback vulnerability type vulnclass,
// Use a placeholder when both are empty - an empty title is never output.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(Unnamed vulnerability)"
}

// Message is the complete content sent by a channel.
type Message struct {
	// The length of a single push is 1; the length of a digest push is a whole batch.
	// Empty slices are illegal, and the caller must ensure at least one.
	Items []Item
	// When Batch=true, the message is rendered according to the summary (change the title, add the time window and number of messages).
	Batch bool
	// WindowMinutes is the summary period (minutes), which is only used for copywriting "near N minutes" when Batch=true.
	// Deliberately pass in the configuration explicitly instead of calculating time.Since when rendering: rendering remains deterministic and easy to test.
	WindowMinutes int
	// HomeURL is the platform panel address (global public_base_url); if empty, there will be no panel entry.
	HomeURL string
}
