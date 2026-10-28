package workflow

import (
	"strings"
	"testing"
)

func TestMissingNote(t *testing.T) {
	if got := missingNote([]string{"REST API"}, 1); !strings.Contains(got, "REST API did not exist") {
		t.Errorf("single spec: %q", got)
	}
	got := missingNote([]string{"a", "b", "c", "d", "e", "f"}, 60)
	for _, want := range []string{"6 of 60", "a, b, c, d", "and 2 more"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q is missing %q", got, want)
		}
	}
	if strings.Count(got, "did not exist") != 1 {
		t.Errorf("must be one sentence, not one per spec: %q", got)
	}
}

func TestParseSince(t *testing.T) {
	for in, ok := range map[string]bool{"90d": true, "6m": true, "1y": true, "12w": true, "2160h": true, "": false, "0d": false, "x": false} {
		if _, err := ParseSince(in); (err == nil) != ok {
			t.Errorf("ParseSince(%q) err = %v, want ok=%v", in, err, ok)
		}
	}
}
