package main

import (
	"flag"
	"reflect"
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	day := 24 * time.Hour
	good := map[string]time.Duration{
		"90d": 90 * day, "12w": 84 * day, "6m": 180 * day, "1y": 365 * day,
		" 6M ": 180 * day, "2160h": 90 * day,
	}
	for in, want := range good {
		got, err := parseSince(in)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "0d", "-3d", "abc", "6x", "1.5y"} {
		if _, err := parseSince(bad); err == nil {
			t.Errorf("parseSince(%q) should fail", bad)
		}
	}
}

func TestReorder(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.String("since", "", "")
		fs.String("spec", "", "")
		fs.Bool("json", false, "")
		return fs
	}
	cases := []struct{ in, want []string }{
		{[]string{"stripe", "-since", "3m", "-json"}, []string{"-since", "3m", "-json", "stripe"}},
		{[]string{"-json", "stripe", "-since", "3m"}, []string{"-json", "-since", "3m", "stripe"}},
		{[]string{"-spec=x", "stripe"}, []string{"-spec=x", "stripe"}},
		{[]string{"stripe"}, []string{"stripe"}},
		{[]string{"-since"}, []string{"-since"}},
	}
	for _, tc := range cases {
		if got := reorder(newFS(), tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("reorder(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
