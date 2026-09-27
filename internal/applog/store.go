// Package applog provides a small, bounded, in-memory application log view.
// It intentionally stores only a few bounded metadata fields and never reads
// files from the host.
package applog

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Capacity is the maximum number of entries retained by a Store.
	Capacity = 2000
	// MaxLimit bounds the amount returned by a single query.
	MaxLimit          = 200
	defaultLimit      = 50
	maxLevelBytes     = 16
	maxComponentBytes = 64
	maxQueryBytes     = 128
	maxMessageBytes   = 512
	maxCursorBytes    = 16
)

var (
	ErrInvalidQuery  = errors.New("invalid log query")
	secretAssignment = regexp.MustCompile(`(?i)\b(?:password|passwd|token|secret|authorization|cookie|credential|api[_-]?key|refresh[_-]?token)\b\s*[:=].*`)
	urlValue         = regexp.MustCompile(`(?i)https?://[^\s]+`)
	absolutePath     = regexp.MustCompile(`(?:^|\s)(?:/[[:alnum:]_.-]+(?:/[[:alnum:]_.-]+)+|[A-Za-z]:\\[^\s]+)`)
	relativePath     = regexp.MustCompile(`\b(?:[[:alnum:]_.-]+/){2,}[[:alnum:]_.-]+\b`)
	commandLine      = regexp.MustCompile(`(?i)^(?:ffmpeg|ffprobe|sh|bash|zsh|cmd(?:\.exe)?|powershell|pwsh|curl|wget|python(?:[0-9]+(?:\.[0-9]+)?)?|node|go|docker|git)(?:\s|$)`)
	headerLine       = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}:\s*\S`)
)

// Entry is one safe, structured application log record. It deliberately has
// no fields for request bodies, headers, commands, arbitrary attributes, or
// filesystem locations.
type Entry struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"message"`
}

// Query selects a stable, newest-first page from the bounded log ring. Since
// is inclusive; a zero value disables the time filter.
type Query struct {
	Level     string
	Component string
	Since     time.Time
	Q         string
	Limit     int
	Cursor    string
}

// Page is a bounded result page. NextCursor is empty when there is no next
// page. Total counts all retained entries matching the non-cursor filters.
type Page struct {
	Items      []Entry `json:"items"`
	NextCursor string  `json:"next_cursor"`
	Total      int     `json:"total"`
}

// Store retains the newest Capacity entries in a ring. The mutex protects the
// ring and the monotonically increasing cursor sequence.
type Store struct {
	mu       sync.RWMutex
	entries  [Capacity]Entry
	next     int
	count    int
	sequence uint64
}

// NewStore returns an empty bounded log store.
func NewStore() *Store { return &Store{} }

// Add appends a bounded, sanitized entry. It returns false if the structured
// fields are invalid or the cursor sequence is exhausted. Message values are
// treated as metadata: URLs, path-like values, and common credential-bearing
// assignments are redacted before storage.
func (s *Store) Add(level, component, message string) bool {
	if s == nil || !validLabel(level, maxLevelBytes) || !validLabel(component, maxComponentBytes) {
		return false
	}
	message = sanitizeMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sequence == ^uint64(0) {
		return false
	}
	s.sequence++
	entry := Entry{
		ID:        fmt.Sprintf("%016x", s.sequence),
		At:        time.Now().UTC(),
		Level:     level,
		Component: component,
		Message:   message,
	}
	s.entries[s.next] = entry
	s.next = (s.next + 1) % Capacity
	if s.count < Capacity {
		s.count++
	}
	return true
}

// Query returns a stable newest-first page. Cursor is the ID of the final
// entry in the preceding page, so subsequent pages contain strictly older
// entries even while newer records are appended.
func (s *Store) Query(query Query) (Page, error) {
	if s == nil {
		return Page{}, ErrInvalidQuery
	}
	query, cursorSequence, err := validateQuery(query)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: make([]Entry, 0, query.Limit)}
	hasMore := false

	s.mu.RLock()
	defer s.mu.RUnlock()
	for age := 0; age < s.count; age++ {
		index := s.next - 1 - age
		if index < 0 {
			index += Capacity
		}
		entry := s.entries[index]
		if !matches(entry, query) {
			continue
		}
		page.Total++
		sequence, ok := parseCursor(entry.ID)
		if !ok || (cursorSequence != 0 && sequence >= cursorSequence) {
			continue
		}
		if len(page.Items) == query.Limit {
			hasMore = true
			continue
		}
		page.Items = append(page.Items, entry)
	}
	if hasMore {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

// ParseQuery validates the URL query for GET /api/logs. Unknown and repeated
// parameters are rejected instead of silently choosing one value.
func ParseQuery(values url.Values) (Query, error) {
	allowed := map[string]bool{
		"level": true, "component": true, "since": true, "q": true,
		"limit": true, "cursor": true,
	}
	for key, list := range values {
		if !allowed[key] || len(list) != 1 || list[0] == "" {
			return Query{}, ErrInvalidQuery
		}
	}
	query := Query{Limit: defaultLimit}
	if value, ok := values["level"]; ok {
		query.Level = value[0]
	}
	if value, ok := values["component"]; ok {
		query.Component = value[0]
	}
	if value, ok := values["q"]; ok {
		query.Q = value[0]
	}
	if value, ok := values["since"]; ok {
		parsed, err := time.Parse(time.RFC3339Nano, value[0])
		if err != nil {
			return Query{}, ErrInvalidQuery
		}
		query.Since = parsed
	}
	if value, ok := values["limit"]; ok {
		limit, err := strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > MaxLimit {
			return Query{}, ErrInvalidQuery
		}
		query.Limit = limit
	}
	if value, ok := values["cursor"]; ok {
		query.Cursor = value[0]
	}
	validated, _, err := validateQuery(query)
	return validated, err
}

func validateQuery(query Query) (Query, uint64, error) {
	if query.Limit == 0 {
		query.Limit = defaultLimit
	}
	if query.Limit < 1 || query.Limit > MaxLimit ||
		(query.Level != "" && !validLabel(query.Level, maxLevelBytes)) ||
		(query.Component != "" && !validLabel(query.Component, maxComponentBytes)) ||
		len(query.Q) > maxQueryBytes || !utf8.ValidString(query.Q) || containsControl(query.Q) {
		return Query{}, 0, ErrInvalidQuery
	}
	cursorSequence := uint64(0)
	if query.Cursor != "" {
		if len(query.Cursor) > maxCursorBytes {
			return Query{}, 0, ErrInvalidQuery
		}
		parsed, ok := parseCursor(query.Cursor)
		if !ok {
			return Query{}, 0, ErrInvalidQuery
		}
		cursorSequence = parsed
	}
	return query, cursorSequence, nil
}

func matches(entry Entry, query Query) bool {
	if query.Level != "" && !strings.EqualFold(entry.Level, query.Level) {
		return false
	}
	if query.Component != "" && !strings.EqualFold(entry.Component, query.Component) {
		return false
	}
	if !query.Since.IsZero() && entry.At.Before(query.Since) {
		return false
	}
	if query.Q != "" {
		needle := strings.ToLower(query.Q)
		haystack := strings.ToLower(entry.Level + " " + entry.Component + " " + entry.Message)
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func parseCursor(value string) (uint64, bool) {
	if len(value) != maxCursorBytes {
		return 0, false
	}
	sequence, err := strconv.ParseUint(value, 16, 64)
	return sequence, err == nil && sequence != 0 && fmt.Sprintf("%016x", sequence) == value
}

func validLabel(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func sanitizeMessage(value string) string {
	if strings.ContainsAny(value, "\r\n") {
		return "[multiline content omitted]"
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") || commandLine.MatchString(trimmed) || headerLine.MatchString(trimmed) {
		return "[content omitted]"
	}
	value = strings.ToValidUTF8(value, "�")
	var clean strings.Builder
	clean.Grow(min(len(value), maxMessageBytes))
	space := false
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if clean.Len() > 0 {
				space = true
			}
			continue
		}
		if space {
			clean.WriteByte(' ')
			space = false
		}
		clean.WriteRune(r)
	}
	message := strings.TrimSpace(clean.String())
	message = secretAssignment.ReplaceAllString(message, "[sensitive value redacted]")
	message = urlValue.ReplaceAllString(message, "[URL redacted]")
	message = absolutePath.ReplaceAllString(message, " [path redacted]")
	message = relativePath.ReplaceAllString(message, "[path redacted]")
	return truncateUTF8(strings.TrimSpace(message), maxMessageBytes)
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
