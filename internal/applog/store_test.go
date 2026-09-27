package applog

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStoreBoundsAndNewestFirstOrder(t *testing.T) {
	store := NewStore()
	for i := 0; i < Capacity+3; i++ {
		if !store.Add("info", "server", fmt.Sprintf("entry %d", i)) {
			t.Fatalf("Add(%d) failed", i)
		}
	}

	query := Query{Limit: MaxLimit}
	var ids []string
	for {
		page, err := store.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, idsOf(page.Items)...)
		if page.NextCursor == "" {
			if page.Total != Capacity {
				t.Fatalf("total = %d, want ring capacity %d", page.Total, Capacity)
			}
			break
		}
		query.Cursor = page.NextCursor
	}
	if len(ids) != Capacity {
		t.Fatalf("retained %d entries, want %d", len(ids), Capacity)
	}
	if ids[0] != fmt.Sprintf("%016x", Capacity+3) || ids[len(ids)-1] != fmt.Sprintf("%016x", 4) {
		t.Fatalf("unexpected ring boundaries: newest=%q oldest=%q", ids[0], ids[len(ids)-1])
	}
	for index := 1; index < len(ids); index++ {
		if compareHex(ids[index-1], ids[index]) <= 0 {
			t.Fatalf("IDs are not strictly newest-first at %d: %q then %q", index, ids[index-1], ids[index])
		}
	}
}

func TestQueryFiltersAndStableCursor(t *testing.T) {
	store := NewStore()
	store.Add("info", "server", "listener ready")
	store.Add("warn", "acquire", "retry scheduled")
	store.Add("error", "acquire", "segment unavailable")

	page, err := store.Query(Query{Level: "ERROR", Component: "acquire", Q: "SEGMENT", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Message != "segment unavailable" || page.NextCursor != "" {
		t.Fatalf("filtered query = %#v", page)
	}

	first, err := store.Query(Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor != first.Items[1].ID {
		t.Fatalf("first page = %#v", first)
	}
	store.Add("info", "server", "newer concurrent entry")
	second, err := store.Query(Query{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Message != "listener ready" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
	if second.Items[0].ID == first.Items[0].ID || second.Items[0].ID == first.Items[1].ID {
		t.Fatal("cursor page repeated an entry")
	}
}

func TestQuerySinceIsInclusive(t *testing.T) {
	store := NewStore()
	store.Add("info", "server", "ready")
	cutoff := time.Now().UTC().Add(time.Second)
	page, err := store.Query(Query{Since: cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("future since returned %d entries", len(page.Items))
	}
}

func TestParseQueryRejectsInvalidAndRepeatedParameters(t *testing.T) {
	badQueries := []url.Values{
		{"unknown": {"value"}},
		{"level": {"info", "error"}},
		{"component": {strings.Repeat("x", maxComponentBytes+1)}},
		{"q": {strings.Repeat("x", maxQueryBytes+1)}},
		{"q": {"line\nbreak"}},
		{"since": {"yesterday"}},
		{"limit": {"0"}},
		{"limit": {"201"}},
		{"limit": {"1", "2"}},
		{"cursor": {"../../private"}},
		{"cursor": {strings.Repeat("f", maxCursorBytes+1)}},
		{"cursor": {"000000000000000A"}},
		{"q": {""}},
	}
	for i, values := range badQueries {
		if _, err := ParseQuery(values); err == nil {
			t.Errorf("ParseQuery case %d accepted %#v", i, values)
		}
	}
}

func TestAddBoundsUTF8AndRedactsSensitiveMetadata(t *testing.T) {
	store := NewStore()
	if !store.Add("warn", "acquire", "fetch failed: https://media.example/segment?sig=secret") {
		t.Fatal("Add rejected valid labels")
	}
	if !store.Add("error", "storage", "failed at /Users/alice/private/recording.json") {
		t.Fatal("Add rejected valid labels")
	}
	if !store.Add("error", "adapterhost", "Authorization: Bearer very-secret-value") {
		t.Fatal("Add rejected valid labels")
	}
	if !store.Add("info", "derivative", "ffmpeg -i /tmp/input.ts -c copy output.mkv") {
		t.Fatal("Add rejected valid labels")
	}
	if !store.Add("debug", "server", "{\"request_body\":\"sensitive\"}") {
		t.Fatal("Add rejected valid labels")
	}
	if store.Add("bad level", "server", "message") {
		t.Fatal("Add accepted an invalid structured level")
	}
	page, err := store.Query(Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, item := range page.Items {
		joined += item.Message + "\n"
		if len(item.Message) > maxMessageBytes {
			t.Fatalf("message exceeded cap: %d", len(item.Message))
		}
		if !utf8.ValidString(item.Message) {
			t.Fatalf("stored invalid UTF-8: %q", item.Message)
		}
	}
	for _, sensitive := range []string{"media.example", "sig=secret", "/Users/alice", "private/recording", "very-secret-value", "ffmpeg -i", "request_body", "sensitive"} {
		if strings.Contains(joined, sensitive) {
			t.Errorf("sensitive substring %q was retained in %q", sensitive, joined)
		}
	}

	long := strings.Repeat("한", maxMessageBytes)
	store.Add("info", "server", long)
	page, err = store.Query(Query{Limit: 1})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Message) > maxMessageBytes || !utf8.ValidString(page.Items[0].Message) {
		t.Fatalf("bounded UTF-8 message = %#v, err=%v", page, err)
	}
}

func TestConcurrentAddAndQuery(t *testing.T) {
	store := NewStore()
	var writers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		writers.Add(1)
		go func(worker int) {
			defer writers.Done()
			for index := 0; index < 400; index++ {
				if !store.Add("info", "worker", fmt.Sprintf("worker %d entry %d", worker, index)) {
					t.Errorf("Add failed for worker %d entry %d", worker, index)
					return
				}
			}
		}(worker)
	}
	var readers sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for index := 0; index < 50; index++ {
				if _, err := store.Query(Query{Limit: 25}); err != nil {
					t.Errorf("Query failed: %v", err)
					return
				}
			}
		}()
	}
	writers.Wait()
	readers.Wait()
	page, err := store.Query(Query{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != Capacity {
		t.Fatalf("final total = %d, want %d", page.Total, Capacity)
	}
}

func idsOf(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func compareHex(left, right string) int {
	leftValue, _ := strconv.ParseUint(left, 16, 64)
	rightValue, _ := strconv.ParseUint(right, 16, 64)
	if leftValue < rightValue {
		return -1
	}
	if leftValue > rightValue {
		return 1
	}
	return 0
}
