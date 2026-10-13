package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This document locks down the capabilities boundaries of the generic webhook template.
//
// This is the only place in the package where "user-supplied strings will be evaluated as code", so be clear about what it can do.
// What can't be done, and use tests to fix these properties - otherwise someone will conveniently add a template context in the future
// method, or add a readFile in FuncMap, the capabilities will be silently expanded, and diff looks like
// Just a harmless little function.

// TestTemplateContextHasNoMethods is the most important one.
//
// text/template will call the export method ({{.Foo}} can both get fields and call methods). So the template context
// As long as you have access to **any** type with exported methods, you are exposing those methods to the template author.
// The context of this feature is intentionally all pure data (only exported fields, zero methods).
//
// If this fails: It means someone has added methods to webhookTemplateData / webhookItem.
// Before deciding to release it, first think about whether that method can be used by the template to read things you don't want to expose.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s exposes %d methods (%s): text/template can call them,"+
				"It is equivalent to opening up the capabilities of these methods to template authors.", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal locks the set of functions exposed to the template.
//
// Each additional function in FuncMap brings an additional capability. Currently there is only json/jsons, which is used to serialize values.
// into JSON fragments - files cannot be read, requests cannot be made, and commands cannot be executed.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template function set changed: got %v, expected %v. Before adding a new function, please confirm that it will not expand the capabilities."+
			"(Cannot read and write files, cannot initiate network requests, and cannot execute commands)", got, want)
	}
}

// TestTemplateCannotReachUnknownData covers out-of-bounds access in templates:
// Accessing something that does not exist must fail, rather than echoing something; and the failure message must not bring out internal data.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("Accessing a non-existent field should result in an error")
	}
	// The actual content in the template context (vulnerability title/summary) cannot appear in the error.
	for _, leak := range []string{"SQL injection", "Parameter id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("Template error reveals message content %q: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently Wrong template writing is a configuration error, and retrying will not heal itself.
// If it is determined to be retryable, a bad template will cause each delivery to run three rounds of evasion in vain.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("Template syntax errors should be blocked on save")
	}
	// Even if the verification is bypassed and delivered directly, a permanent failure must be determined instead of repeated retries.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("A bad template should fail permanently and get %v", err)
	}
}

// TestTemplateCanOnlyProduceJSON overrides the constraint that "the template rendering result must be legal JSON".
// It incidentally blocks the use of "using templates to generate plain text to trigger other protocols".
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// Legal templates can pass.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("Legal template should pass verification: %v", err)
	}
	// MUST reject rendering of non-JSON (rather than emitting it as-is).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("Rendering non-JSON should fail permanently and get %v", err)
	}
	if !strings.Contains(err.Error(), "legal JSON") {
		t.Errorf("The error message should indicate a JSON issue, getting %v", err)
	}
}
