package adapterproto

import (
	"encoding/json"
	"fmt"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Descriptor struct {
	ID                  string         `json:"id"`
	Name                string         `json:"name"`
	Version             string         `json:"version"`
	ProtocolVersion     int            `json:"protocol_version"`
	Capabilities        []string       `json:"capabilities,omitempty"`
	InputSchema         Schema         `json:"input_schema"`
	ConfigurationSchema Schema         `json:"configuration_schema"`
	ResourceTypes       []ResourceType `json:"resource_types,omitempty"`
	MediaTypes          []string       `json:"media_types"`
}

type Schema struct {
	Fields []Field `json:"fields"`
}

type Field struct {
	Key         string          `json:"key"`
	Control     string          `json:"control"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Constraints *Constraints    `json:"constraints,omitempty"`
	Options     []Option        `json:"options,omitempty"`
	VisibleWhen json.RawMessage `json:"visible_when,omitempty"`
}

type Constraints struct {
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	MinItems  *int     `json:"min_items,omitempty"`
	MaxItems  *int     `json:"max_items,omitempty"`
}

type Option struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

type ResourceType struct {
	Type                string   `json:"type"`
	ParentTypes         []string `json:"parent_types,omitempty"`
	ConfigurationSchema Schema   `json:"configuration_schema,omitempty"`
}

type ResourceRef struct {
	Type   string       `json:"resource_type"`
	ID     string       `json:"resource_id"`
	Parent *ResourceRef `json:"parent,omitempty"`
}

type Resource struct {
	ResourceRef
	DisplayName string                     `json:"display_name,omitempty"`
	Attributes  map[string]json.RawMessage `json:"attributes,omitempty"`
}

type ResolveParams struct {
	Input         json.RawMessage            `json:"input"`
	Resource      *ResourceRef               `json:"resource,omitempty"`
	Configuration map[string]json.RawMessage `json:"configuration,omitempty"`
	Secrets       map[string]string          `json:"secrets,omitempty"`
}

type MediaSource struct {
	Type          string            `json:"type"`
	ManifestURL   string            `json:"manifest_url"`
	Headers       map[string]string `json:"headers,omitempty"`
	RequestPolicy *RequestPolicy    `json:"request_policy,omitempty"`
	SessionRef    string            `json:"session_ref,omitempty"`
	Refresh       json.RawMessage   `json:"refresh,omitempty"`
	Metadata      json.RawMessage   `json:"metadata,omitempty"`
}

// RequestPolicy limits where Core may forward adapter-supplied media headers.
// It authorizes header forwarding only; network and SSRF policy remains owned
// by Core. An omitted policy or header_forwarding block is restrictive.
type RequestPolicy struct {
	HeaderForwarding *HeaderForwardingPolicy `json:"header_forwarding,omitempty"`
}

// HeaderForwardingPolicy currently applies one origin set to all adapter-
// supplied headers. Keeping mode as a string leaves room for future protocol
// extensions such as per-header rules without introducing a policy engine now.
type HeaderForwardingPolicy struct {
	Mode    string   `json:"mode,omitempty"`
	Origins []string `json:"origins,omitempty"`
}

const (
	HeaderForwardingSameOrigin = "same_origin"
	HeaderForwardingAllowlist  = "allowlist"
)

type mediaOrigin struct {
	scheme   string
	hostname string
	port     string
}

func (o mediaOrigin) equal(other mediaOrigin) bool {
	return o.scheme == other.scheme && o.hostname == other.hostname && o.port == other.port
}

// AllowsHeadersFor reports whether the source's adapter-supplied headers may
// be sent to target. Callers must still enforce their independent network
// safety policy before making the request.
func (m MediaSource) AllowsHeadersFor(target *url.URL) bool {
	if target == nil {
		return false
	}
	policy := m.RequestPolicy
	if policy == nil || policy.HeaderForwarding == nil || policy.HeaderForwarding.Mode == "" || policy.HeaderForwarding.Mode == HeaderForwardingSameOrigin {
		manifest, err := parseMediaOrigin(m.ManifestURL)
		if err != nil {
			return false
		}
		targetOrigin, err := originFromURL(target)
		return err == nil && manifest.equal(targetOrigin)
	}
	if policy.HeaderForwarding.Mode != HeaderForwardingAllowlist {
		return false
	}
	targetOrigin, err := originFromURL(target)
	if err != nil {
		return false
	}
	for _, raw := range policy.HeaderForwarding.Origins {
		allowed, err := parseAllowedOrigin(raw)
		if err == nil && allowed.equal(targetOrigin) {
			return true
		}
	}
	return false
}

type InteractionField struct {
	Key         string   `json:"key"`
	Control     string   `json:"control"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

type InteractionMessage struct {
	Type          string             `json:"type"`
	InteractionID string             `json:"interaction_id"`
	Title         string             `json:"title,omitempty"`
	Message       string             `json:"message,omitempty"`
	Fields        []InteractionField `json:"fields,omitempty"`
	Data          json.RawMessage    `json:"data,omitempty"`
}

func (m InteractionMessage) Validate() error {
	if strings.TrimSpace(m.InteractionID) == "" {
		return fmt.Errorf("interaction id is required")
	}
	switch m.Type {
	case "action", "prompt", "secret_prompt", "navigate", "display", "status", "complete", "error":
	default:
		return fmt.Errorf("unsupported interaction message type")
	}
	seen := map[string]bool{}
	for _, field := range m.Fields {
		if field.Key == "" || field.Label == "" || seen[field.Key] {
			return fmt.Errorf("interaction field keys and labels must be nonempty and unique")
		}
		seen[field.Key] = true
		if field.Control != "text" && field.Control != "secret" && field.Control != "number" && field.Control != "boolean" && field.Control != "select" && field.Control != "multi-select" && field.Control != "textarea" {
			return fmt.Errorf("interaction field control is invalid")
		}
		if (field.Control == "select" || field.Control == "multi-select") && len(field.Options) == 0 {
			return fmt.Errorf("interaction select field requires options")
		}
	}
	if len(m.Data) > 0 && !json.Valid(m.Data) {
		return fmt.Errorf("interaction data is invalid JSON")
	}
	return nil
}

func (d Descriptor) Validate() error {
	if strings.TrimSpace(d.ID) == "" || strings.ContainsAny(d.ID, "/\\") {
		return fmt.Errorf("adapter id is invalid")
	}
	if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Version) == "" {
		return fmt.Errorf("adapter name and version are required")
	}
	if d.ProtocolVersion != Version {
		return fmt.Errorf("unsupported protocol version %d", d.ProtocolVersion)
	}
	if err := d.InputSchema.Validate(); err != nil {
		return fmt.Errorf("input schema: %w", err)
	}
	if err := d.ConfigurationSchema.Validate(); err != nil {
		return fmt.Errorf("configuration schema: %w", err)
	}
	validCapabilities := map[string]bool{CapabilityResolve: true, CapabilityStatus: true, CapabilityConfigure: true, CapabilityInteraction: true, CapabilityMetadata: true, CapabilityEvents: true, CapabilityRefresh: true}
	seenCapabilities := map[string]bool{}
	for _, capability := range d.Capabilities {
		if !validCapabilities[capability] || seenCapabilities[capability] {
			return fmt.Errorf("capability is unsupported or duplicated")
		}
		seenCapabilities[capability] = true
	}
	seen := map[string]bool{}
	for _, rt := range d.ResourceTypes {
		if strings.TrimSpace(rt.Type) == "" || seen[rt.Type] {
			return fmt.Errorf("resource type is empty or duplicated")
		}
		seen[rt.Type] = true
		parents := map[string]bool{}
		for _, parent := range rt.ParentTypes {
			if strings.TrimSpace(parent) == "" || parents[parent] {
				return fmt.Errorf("resource parent type is empty or duplicated")
			}
			parents[parent] = true
		}
		if err := rt.ConfigurationSchema.Validate(); err != nil {
			return fmt.Errorf("resource schema: %w", err)
		}
	}
	for _, mt := range d.MediaTypes {
		if mt == "" {
			return fmt.Errorf("media type is empty")
		}
	}
	return nil
}

func ValidateResourceRef(ref *ResourceRef) error {
	seen := map[*ResourceRef]bool{}
	depth := 0
	for current := ref; current != nil; current = current.Parent {
		depth++
		if depth > 64 {
			return fmt.Errorf("resource hierarchy exceeds limit")
		}
		if seen[current] {
			return fmt.Errorf("resource hierarchy contains a cycle")
		}
		seen[current] = true
		if strings.TrimSpace(current.Type) == "" || strings.TrimSpace(current.ID) == "" {
			return fmt.Errorf("resource type and id are required")
		}
	}
	return nil
}

func (s Schema) Validate() error {
	seen := map[string]bool{}
	for _, f := range s.Fields {
		if strings.TrimSpace(f.Key) == "" || seen[f.Key] {
			return fmt.Errorf("field keys must be nonempty and unique")
		}
		seen[f.Key] = true
		if strings.TrimSpace(f.Label) == "" {
			return fmt.Errorf("field %q label is required", f.Key)
		}
		switch f.Control {
		case "text", "secret", "number", "boolean", "select", "multi-select", "textarea", "action", "status":
		default:
			return fmt.Errorf("field %q has unsupported control", f.Key)
		}
		if (f.Control == "select" || f.Control == "multi-select") && len(f.Options) == 0 {
			return fmt.Errorf("field %q requires options", f.Key)
		}
		if f.Control == "secret" && len(f.Default) > 0 {
			return fmt.Errorf("field %q cannot declare a secret default", f.Key)
		}
		if f.Constraints != nil {
			c := f.Constraints
			if c.Min != nil && c.Max != nil && *c.Min > *c.Max {
				return fmt.Errorf("field %q has inverted numeric bounds", f.Key)
			}
			if c.MinLength != nil && *c.MinLength < 0 || c.MaxLength != nil && *c.MaxLength < 0 || c.MinLength != nil && c.MaxLength != nil && *c.MinLength > *c.MaxLength {
				return fmt.Errorf("field %q has invalid length constraints", f.Key)
			}
			if c.Pattern != "" {
				if _, err := regexp.Compile(c.Pattern); err != nil {
					return fmt.Errorf("field %q has invalid pattern", f.Key)
				}
			}
		}
		if len(f.VisibleWhen) > 0 && !json.Valid(f.VisibleWhen) {
			return fmt.Errorf("field %q has invalid visibility condition", f.Key)
		}
		if len(f.Default) > 0 && !json.Valid(f.Default) {
			return fmt.Errorf("field %q has invalid default", f.Key)
		}
	}
	return nil
}

func ValidateObject(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("input object is required")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return fmt.Errorf("input must be a JSON object")
	}
	return nil
}

func ValidateValues(schema Schema, values map[string]json.RawMessage) error {
	fields := map[string]Field{}
	for _, f := range schema.Fields {
		if f.Control != "action" && f.Control != "status" {
			fields[f.Key] = f
		}
	}
	for key, raw := range values {
		field, ok := fields[key]
		if !ok {
			return fmt.Errorf("unknown configuration key %q", key)
		}
		if err := validateValue(field, raw); err != nil {
			return fmt.Errorf("invalid configuration value for %q", key)
		}
	}
	for key, field := range fields {
		if field.Required {
			if _, ok := values[key]; !ok && len(field.Default) == 0 {
				return fmt.Errorf("required configuration key %q is missing", key)
			}
		}
	}
	return nil
}

func validateValue(f Field, raw json.RawMessage) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON")
	}
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return err
	}
	switch f.Control {
	case "text", "secret", "textarea":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if f.Constraints != nil {
			c := f.Constraints
			if c.MinLength != nil && len([]rune(s)) < *c.MinLength || c.MaxLength != nil && len([]rune(s)) > *c.MaxLength {
				return fmt.Errorf("length constraint")
			}
			if c.Pattern != "" && !regexp.MustCompile(c.Pattern).MatchString(s) {
				return fmt.Errorf("pattern constraint")
			}
		}
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("expected number")
		}
		value, err := n.Float64()
		if err != nil {
			return err
		}
		if f.Constraints != nil && (f.Constraints.Min != nil && value < *f.Constraints.Min || f.Constraints.Max != nil && value > *f.Constraints.Max) {
			return fmt.Errorf("range constraint")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	case "select":
		if !optionContains(f.Options, v) {
			return fmt.Errorf("invalid option")
		}
	case "multi-select":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		if f.Constraints != nil && (f.Constraints.MinItems != nil && len(arr) < *f.Constraints.MinItems || f.Constraints.MaxItems != nil && len(arr) > *f.Constraints.MaxItems) {
			return fmt.Errorf("item count constraint")
		}
		for _, item := range arr {
			if !optionContains(f.Options, item) {
				return fmt.Errorf("invalid option")
			}
		}
	}
	return nil
}

func optionContains(options []Option, candidate any) bool {
	a, _ := json.Marshal(candidate)
	for _, option := range options {
		b, _ := json.Marshal(option.Value)
		if string(a) == string(b) {
			return true
		}
	}
	return false
}

func ValidateMediaSource(media MediaSource, supported []string) error {
	if media.Type == "" || media.ManifestURL == "" {
		return fmt.Errorf("media type and manifest URL are required")
	}
	ok := false
	for _, v := range supported {
		if v == media.Type {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("unsupported media type")
	}
	u, err := url.ParseRequestURI(media.ManifestURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid media manifest URL")
	}
	for key, value := range media.Headers {
		if textproto.CanonicalMIMEHeaderKey(key) == "" || !validHeaderValue(value) {
			return fmt.Errorf("invalid media header")
		}
	}
	if err := validateRequestPolicy(media.RequestPolicy); err != nil {
		return fmt.Errorf("invalid request policy: %w", err)
	}
	return nil
}

func validateRequestPolicy(policy *RequestPolicy) error {
	if policy == nil || policy.HeaderForwarding == nil {
		return nil
	}
	forwarding := policy.HeaderForwarding
	mode := forwarding.Mode
	if mode == "" {
		mode = HeaderForwardingSameOrigin
	}
	switch mode {
	case HeaderForwardingSameOrigin:
		if len(forwarding.Origins) != 0 {
			return fmt.Errorf("same_origin mode cannot declare extra origins")
		}
	case HeaderForwardingAllowlist:
		if len(forwarding.Origins) == 0 {
			return fmt.Errorf("allowlist mode requires at least one origin")
		}
		seen := map[mediaOrigin]bool{}
		for _, raw := range forwarding.Origins {
			origin, err := parseAllowedOrigin(raw)
			if err != nil {
				return fmt.Errorf("invalid allowed origin %q", raw)
			}
			if seen[origin] {
				return fmt.Errorf("allowed origins must be unique")
			}
			seen[origin] = true
		}
	default:
		return fmt.Errorf("unsupported header forwarding mode %q", forwarding.Mode)
	}
	return nil
}

func parseAllowedOrigin(raw string) (mediaOrigin, error) {
	if raw == "" || strings.ContainsAny(raw, "\\\r\n\t #?") {
		return mediaOrigin{}, fmt.Errorf("origin must be an HTTP(S) origin URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return mediaOrigin{}, fmt.Errorf("origin must not include path, query, or fragment")
	}
	return originFromURL(u)
}

func parseMediaOrigin(raw string) (mediaOrigin, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return mediaOrigin{}, fmt.Errorf("invalid media URL")
	}
	return originFromURL(u)
}

func originFromURL(u *url.URL) (mediaOrigin, error) {
	if u == nil || u.Opaque != "" || u.User != nil || !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") {
		return mediaOrigin{}, fmt.Errorf("URL does not have a valid HTTP(S) origin")
	}
	scheme := strings.ToLower(u.Scheme)
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 0 || portNumber > 65535 {
			return mediaOrigin{}, fmt.Errorf("URL port is invalid")
		}
		port = strconv.Itoa(portNumber)
	}
	return mediaOrigin{scheme: scheme, hostname: hostname, port: port}, nil
}

func validHeaderValue(value string) bool {
	for _, r := range value {
		if r == 0x7f || r < 0x20 && r != '\t' {
			return false
		}
	}
	return true
}
