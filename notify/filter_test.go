package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, fields of the wrong type - all must degenerate into a zero-valued Filter,
	// That is "no filtering". This invariant is the starting point of “I would rather push more than miss”:
	// Once this is changed to error reporting or semi-parsing, if the user mismatches a character, all high-risk notifications will be silently discarded.
	cases := []struct {
		name string
		raw  string
	}{
		{"Empty input", ""},
		{"Illegal JSON", `{not json`},
		{"Truncated JSON", `{"min_severity":`},
		{"type mismatch", `{"min_severity": 123, "task_ids": "abc"}`},
		{"The top level is an array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("The malformed configuration should degenerate into a zero-valued Filter, resulting in %+v", f)
			}
			// A zero value Filter must hit any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("A zero value Filter should hit all events")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// The unknown level ordinal is 0 and should be blocked by any non-empty threshold (do not push when in doubt).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: expect %v and get %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL injection",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"Empty range = no limit", Filter{}, true},
		{"mission hit", Filter{TaskIDs: []int64{7}}, true},
		{"Mission missed", Filter{TaskIDs: []int64{8}}, false},
		{"Multiple selection of tasks including hits", Filter{TaskIDs: []int64{8, 7}}, true},
		{"Assets intersect", Filter{AssetIDs: []int64{20, 99}}, true},
		{"No intersection of assets", Filter{AssetIDs: []int64{99}}, false},
		{"Task and asset hit at the same time", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"Task hits but asset misses", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("Expect %v get %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include is empty = include all", Filter{}, "any type", true},
		{"include hit", Filter{VulnClassInclude: []string{"SQL"}}, "SQL injection", true},
		{"include miss", Filter{VulnClassInclude: []string{"command execution"}}, "SQL injection", false},
		{"include any hit of multiple words", Filter{VulnClassInclude: []string{"command execution", "SQL"}}, "SQL injection", true},
		{"Case insensitive", Filter{VulnClassInclude: []string{"sql"}}, "SQL injection", true},
		{"exclude Exclude when hit", Filter{VulnClassExclude: []string{"information leakage"}}, "information leakage", false},
		{"exclude If not hit, let it pass", Filter{VulnClassExclude: []string{"information leakage"}}, "SQL injection", true},
		// Exclusion takes precedence over inclusion: simultaneous hits should result in an out.
		{"Exclusion takes precedence over inclusion", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"Inject"},
		}, "SQL injection", false},
		// Pure whitespace keywords should be ignored, otherwise they will degenerate into "match all strings containing spaces".
		{"Blank keywords are ignored", Filter{VulnClassInclude: []string{"", "  "}}, "SQL injection", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("Expect %v get %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Default off: What most people refer to as "push vulnerabilities" refers to the discovery of new vulnerabilities, not a status account.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("State change events should be skipped if not enabled")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("After turning on on_status_change, the status change event should hit")
	}
	// Creation events are not affected by on_status_change.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("Create events should not rely on on_status_change")
	}
}
