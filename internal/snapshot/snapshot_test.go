package snapshot

import (
	"testing"
	"time"
)

func at(day int) time.Time { return time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC) }

func TestSaveSkipsIdenticalCopies(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if ok, err := s.Save("Notion", "REST API", []byte("v1"), at(1)); !ok || err != nil {
		t.Fatalf("first save = %v, %v", ok, err)
	}
	if ok, _ := s.Save("Notion", "REST API", []byte("v1"), at(2)); ok {
		t.Error("an unchanged spec should not create a new snapshot")
	}
	if ok, _ := s.Save("Notion", "REST API", []byte("v2"), at(3)); !ok {
		t.Error("a changed spec should be saved")
	}
	snaps, _ := s.List("Notion", "REST API")
	if len(snaps) != 2 {
		t.Errorf("have %d snapshots, want 2", len(snaps))
	}
}

func TestSaveRefusesEmpty(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if _, err := s.Save("x", "y", nil, at(1)); err == nil {
		t.Error("an empty download must not become history")
	}
}

func TestAtOrBefore(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	s.Save("p", "s", []byte("day5"), at(5))
	s.Save("p", "s", []byte("day10"), at(10))
	s.Save("p", "s", []byte("day20"), at(20))

	body, when, exact, _ := s.AtOrBefore("p", "s", at(12))
	if string(body) != "day10" || !when.Equal(at(10)) || !exact {
		t.Errorf("at day 12 got %q %v exact=%v, want the day 10 copy", body, when, exact)
	}
	body, when, exact, _ = s.AtOrBefore("p", "s", at(1))
	if string(body) != "day5" || !when.Equal(at(5)) || exact {
		t.Errorf("before all history got %q %v exact=%v, want the oldest copy (day 5) flagged as not exact", body, when, exact)
	}
	if body, _, _, _ := s.AtOrBefore("nobody", "s", at(1)); body != nil {
		t.Error("an unknown provider should have no history")
	}
}

func TestSlug(t *testing.T) {
	if slug("Jira Cloud / REST API") != "jira-cloud-rest-api" {
		t.Errorf("slug = %q", slug("Jira Cloud / REST API"))
	}
}

func TestSave_ReorderedDownloadsAreNotNewVersions(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	a := []byte(`{"openapi":"3.0.0","paths":{"/a":{"get":{"security":[{"oauth":["read","write","admin"]}]}}},"tags":["x","y"]}`)
	b := []byte(`{"tags":["y","x"],"paths":{"/a":{"get":{"security":[{"oauth":["admin","read","write"]}]}}},"openapi":"3.0.0"}`)
	if ok, _ := s.Save("x", "api", a, at(1)); !ok {
		t.Fatal("first save failed")
	}
	if ok, _ := s.Save("x", "api", b, at(2)); ok {
		t.Error("the same spec with keys and scopes reordered was saved as a new snapshot")
	}
	changed := []byte(`{"openapi":"3.0.0","paths":{"/a":{"get":{"security":[{"oauth":["read","write","admin","extra"]}]}}},"tags":["x","y"]}`)
	if ok, _ := s.Save("x", "api", changed, at(3)); !ok {
		t.Error("a genuinely different spec must be saved")
	}
}
