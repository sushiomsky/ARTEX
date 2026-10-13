package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel is a universal webhook adapter: user-defined URLs, methods, request headers and JSON templates.
// Its existence eliminates the need to write an implementation for each of Slack / Mattermost / Discord / self-built systems——
// Those platforms can all be covered by a configurable template.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// There is no official limit for general webhook. Returning 0 means no flow limit by default, which can be customized by the user according to the capabilities of the peer.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Mask url and headers: The target address itself often contains token, and the custom header usually contains authentication credentials.
// Both will appear in the interface echo, so they must be blocked.
// The price is that if you want to change one of the headers during editing, you need to refill the entire group of headers (the mask value will be interpreted as "keep the original value")——
// This trade-off is deliberate: I would rather fill it out one more time than echo the credentials to the browser.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// destination is url. When changing the URL, the headers must be re-expressed - otherwise the original Authorization header
// It will be sent to the new address unchanged, which is the main path of mask bypass.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate is the request body when the template is not filled in: a straightforward JSON structure,
// Covers most self-built receivers that "receive a piece of JSON and store it in the database".
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData is the context of the template exposed to the user.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt is the delivery time (RFC3339) for the receiving end to record.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel is a readable description of the status change, such as "Pending → Fixed"; it is empty when there is no status change.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("Destination URL missing")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("Invalid target URL: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("Unsupported method %s (can use GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("Request body template syntax error: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET does not have a request body: stuffing the content into the query is beyond the scope of the template and does not comply with GET semantics.
	// Therefore, GET is only suitable for receivers such as "hook is triggered when hit".
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// The template renders JSON in the form of a string, which is converted into json.RawMessage and sent out as it is.
		// Avoid double escaping to put the user's carefully constructed structure into a JSON string.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("The request body template rendering result is not legal JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Overriding is allowed, but applied after headers to ensure explicit configuration takes precedence.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// General Webhook does not truncate the body (the receiving end is the user's own service, and the size is determined by body_template),
	// Therefore the entire batch is considered delivered.
	return len(m.Items), nil
}

// renderWebhookBody Renders the request body using the user template (or default template).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("Request body template syntax error: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("Failed to render request body template: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate parses the template.
//
// missingkey=zero causes missing map keys to render as zero values instead of reporting an error - but the context of this file is a structure,
// The main function is to prevent range from causing errors when .Items is empty. What you really need to guard against is .Items being nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs are helper functions exposed to templates.
var webhookTemplateFuncs = template.FuncMap{
	// json serializes any value into JSON.
	//
	// This function is not a nice-to-have but a necessity: omitting it, the user can only write {{.Title}} for direct interpolation,
	// As long as there are quotation marks or line breaks in the vulnerability title, the entire request body is no longer valid JSON - the receiving end will
	// It was rejected, and the error message pointed to "JSON parsing failed." It was completely unexpected that there was a quotation mark in the title.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons is used to embed a JSON fragment inside another JSON string value (do a layer of string escaping).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Remove outer quotes: The caller decides whether to add quotes or not.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
