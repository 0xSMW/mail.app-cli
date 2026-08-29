package mail

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func installFakeSQLite(t *testing.T, response string) string {
	t.Helper()
	binDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	script := "#!/bin/sh\n" +
		"if [ -n \"$SMART_SQL_FILE\" ]; then printf '%s' \"$4\" > \"$SMART_SQL_FILE\"; fi\n" +
		"printf '%s\\n' \"$SMART_SQL_RESPONSE\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake sqlite3: %v", err)
	}
	t.Setenv("SMART_SQL_RESPONSE", response)
	return binDir
}

func TestListSmartMailboxesAlwaysReturnsTodayWhenEmpty(t *testing.T) {
	installFakeSQLite(t, `[{"TotalCount":0,"UnreadCount":0}]`)
	boxes, err := NewClient().ListSmartMailboxes()
	if err != nil {
		t.Fatalf("ListSmartMailboxes: %v", err)
	}
	if len(boxes) != 1 || boxes[0].Name != "Today" || boxes[0].TotalCount != 0 || boxes[0].Unread != 0 {
		t.Fatalf("ListSmartMailboxes = %#v, want zero-count Today", boxes)
	}
}

func TestListSmartMailboxesDoesNotUseUnsupportedMailSmartMailboxAPI(t *testing.T) {
	binDir := installFakeSQLite(t, `[{"TotalCount":2,"UnreadCount":1}]`)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '%s\\n' 'Message not understood' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	boxes, err := NewClient().ListSmartMailboxes()
	if err != nil {
		t.Fatalf("ListSmartMailboxes: %v", err)
	}
	if len(boxes) != 1 || boxes[0].TotalCount != 2 || boxes[0].Unread != 1 {
		t.Fatalf("ListSmartMailboxes = %#v, want Today from the index", boxes)
	}
}

func TestListSmartMailboxesReturnsCapabilityUnavailableWhenIndexDisabled(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	_, err := NewClient().ListSmartMailboxes()
	if err == nil {
		t.Fatal("ListSmartMailboxes error = nil, want capability error")
	}
	capability, ok := err.(*CapabilityError)
	if !ok || capability.Capability != "Today smart mailbox" || capability.Status != CapabilityUnavailable {
		t.Fatalf("ListSmartMailboxes error = %#v, want unavailable Today capability", err)
	}
}

func TestTodaySmartMailboxUsesReceivedDateInHalfOpenLocalDay(t *testing.T) {
	loc := time.FixedZone("test", 7*60*60)
	now := time.Date(2026, time.August, 29, 13, 0, 0, 0, loc)
	start, end := todayBounds(now, loc)

	t.Setenv("HOME", t.TempDir())
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	queryFile := filepath.Join(t.TempDir(), "query.sql")
	t.Setenv("SMART_SQL_FILE", queryFile)
	t.Setenv("SMART_SQL_RESPONSE", `[{"TotalCount":3,"UnreadCount":2}]`)
	script := "#!/bin/sh\nprintf '%s' \"$4\" > \"$SMART_SQL_FILE\"\nprintf '%s\\n' \"$SMART_SQL_RESPONSE\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake sqlite3: %v", err)
	}

	box, err := NewClient().todaySmartMailbox(now, loc)
	if err != nil {
		t.Fatalf("todaySmartMailbox: %v", err)
	}
	if box.TotalCount != 3 || box.Unread != 2 {
		t.Fatalf("todaySmartMailbox = %#v", box)
	}
	query, err := os.ReadFile(queryFile)
	if err != nil {
		t.Fatalf("read query: %v", err)
	}
	text := string(query)
	if !strings.Contains(text, "m.date_received >= "+strconv.FormatInt(start.Unix(), 10)) ||
		!strings.Contains(text, "m.date_received < "+strconv.FormatInt(end.Unix(), 10)) {
		t.Fatalf("query %q does not use [%d, %d)", text, start.Unix(), end.Unix())
	}
	if strings.Contains(text, "date_last_viewed") {
		t.Fatalf("query %q uses view time instead of received time", text)
	}
}

func TestTodayBoundsRespectLocalMidnightAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	start, end := todayBounds(time.Date(2026, time.March, 8, 12, 0, 0, 0, loc), loc)
	if start.Hour() != 0 || end.Hour() != 0 || start.Day() != 8 || end.Day() != 9 {
		t.Fatalf("bounds = %v, %v; want local midnights on Mar 8 and Mar 9", start, end)
	}
	if got := end.Sub(start); got != 23*time.Hour {
		t.Fatalf("DST bounds duration = %v, want 23h", got)
	}

	prior := start.Add(-time.Nanosecond)
	if !prior.Before(start) || !end.After(start) {
		t.Fatalf("invalid half-open bounds: start=%v end=%v", start, end)
	}
}
