package appointment

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsEntriesWaitingListAndNotifications(t *testing.T) {
	root := testTempRoot(t)
	store, err := NewStore(root, AccountKey("https://example.test", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	entry := Entry{EventID: "event-1", ContactID: "contact-1", ContactName: "Ada", Title: "Consult", Start: start, Duration: "17m", Services: []Service{DefaultServices()[1]}}
	if err := store.PutEntry(entry); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.GetEntry("event-1")
	if err != nil || !ok {
		t.Fatalf("GetEntry ok=%t err=%v", ok, err)
	}
	if loaded.ContactID != "contact-1" {
		t.Fatalf("loaded = %#v", loaded)
	}
	services, err := store.DefaultServicesForContact("contact-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].Name != "service2" {
		t.Fatalf("services = %#v", services)
	}
	if err := store.AddWaitingList(WaitingListRequest{ContactID: "contact-1", Days: []string{"2026-01-05"}, Times: []string{"10:00"}, ServiceIDs: []string{"service2"}}); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.WaitingList("contact-1")
	if err != nil || len(waiting) != 1 {
		t.Fatalf("waiting len=%d err=%v", len(waiting), err)
	}
	due, err := store.NotifyDue(time.Now(), 24*time.Hour)
	if err != nil || len(due) != 1 {
		t.Fatalf("due len=%d err=%v", len(due), err)
	}
	if err := store.MarkNotificationSent("event-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	due, err = store.NotifyDue(time.Now(), 24*time.Hour)
	if err != nil || len(due) != 0 {
		t.Fatalf("due after mark len=%d err=%v", len(due), err)
	}
	if err := store.MarkNotificationFailed("event-2", "smtp down", time.Now()); err != nil {
		t.Fatal(err)
	}
	marked, err := store.IsNotificationMarked("event-2")
	if err != nil || !marked {
		t.Fatalf("marked=%t err=%v", marked, err)
	}
}

func testTempRoot(t *testing.T) string {
	t.Helper()
	base := os.Getenv("SUPER_TMP_DIR")
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(filepath.Join(wd, "AGENTS.md")); err == nil {
				base = filepath.Join(wd, "tmp")
				break
			}
			parent := filepath.Dir(wd)
			if parent == wd {
				base = filepath.Join(".", "tmp")
				break
			}
			wd = parent
		}
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "jmap-store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
