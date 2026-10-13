package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// The match/enforce layer for asset intercept rules. asset_intercept.go owns rule
// storage; this file matches a "target asset"'s domains/IPs/URLs against the
// enabled rules. Called by agent tools (add_intent, insert_assets) before
// dispatching an intent / inserting assets — a hit means reject.

// AssetInterceptKindLabel returns the human-readable kind label for agent-facing messages.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "domain (exact)"
	case "exact_ip":
		return "IP (exact)"
	case "exact_url":
		return "URL (exact)"
	case "fuzzy_domain":
		return "domain (fuzzy)"
	case "fuzzy_ip":
		return "IP (fuzzy)"
	case "fuzzy_url":
		return "URL (fuzzy)"
	case "cidr":
		return "CIDR range"
	}
	return kind
}

// Reason returns a readable hit reason, e.g.: hit asset intercept rule [domain (fuzzy): .gov.cn] (note).
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("hit asset intercept rule [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += " (" + note + ")"
	}
	return s
}

// matchOne tests one enabled rule against the given domain/IP/URL candidates, returning the matched value.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules returns the first enabled rule hitting the given domain/IP/URL candidates, plus the matched value.
// Used by insert_assets to match raw input (not-yet-stored assetInputItems).
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates extracts a stored asset's domain/IP/URL match candidates.
// A URL's host is split out and classified so "URL-only" service assets still hit domain/IP rules.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel returns a short asset identifier for agent-facing messages.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("asset#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule reports whether the rule set holds any enabled rule.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision is a "block-then-allow" gate verdict over one candidate set.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // rejection reason (no asset identifier); empty when Allowed=true
}

// EvaluateAssetGate runs the task-level gate:
//  1. Any enabled blockRules hit → reject (intercept reason).
//  2. Else, if allowRules has enabled entries and none hit → reject (out of allowlist scope).
//  3. Else allow.
//
// With empty/unenabled allowRules the allow gate is off (no allowlist: everything passes),
// so "no allow rules configured" never blocks every asset.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "outside the task allowlist scope; testing is not allowed"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit describes one asset rejected by the gate (intercept hit or out of allowlist scope).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // readable reason
}

// Describe returns a readable note: asset info + reason.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules passes through the *DB method of the same name so callers
// holding only an AssetStore (e.g. agent tools) can read the rules too.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept loads assets by id and runs the "block-then-allow" gate on each,
// returning every rejected asset. Block rules = global ∪ task-level block; allow rules
// = task-level allow (this task only). Empty ids return fast. Uses the global GetByIDs
// (no task-scope filtering) so interception can never be weakened by scope.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// No block rules and no enabled allow rules → nothing to decide, allow everything.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
