package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		err    error
		code   int
		stderr string
	}{
		{nil, 0, ""},
		{flag.ErrHelp, 0, ""},
		{usagef("usage: unbroke diff"), 2, "unbroke: usage: unbroke diff\n"},
		{usageError{msg: "flag provided but not defined: -x", shown: true}, 2, ""},
		{exitError{code: 4}, 4, ""},
		{errors.New("boom"), 1, "unbroke: boom\n"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		if got := exitCode(c.err, &b); got != c.code || b.String() != c.stderr {
			t.Errorf("exitCode(%v) = %d, %q; want %d, %q", c.err, got, b.String(), c.code, c.stderr)
		}
	}
}

func TestSubcommandHelpIsNotAnError(t *testing.T) {
	for _, run := range []func([]string, *bytes.Buffer) error{
		func(a []string, w *bytes.Buffer) error { return runScan(a, w) },
		func(a []string, w *bytes.Buffer) error { return runFix(a, w) },
		func(a []string, w *bytes.Buffer) error { return runDiff(a, w) },
		func(a []string, w *bytes.Buffer) error { return runChanges(a, w) },
	} {
		var out bytes.Buffer
		err := run([]string{"-h"}, &out)
		if exitCode(err, &bytes.Buffer{}) != 0 {
			t.Errorf("-h gave %v", err)
		}
	}
	if err := runScan([]string{"-nope"}, &bytes.Buffer{}); exitCode(err, &bytes.Buffer{}) != 2 {
		t.Errorf("an unknown flag is bad usage (2), got %v", err)
	}
}

func TestReorder_DoubleDashEndsFlags(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.Bool("json", false, "")
	got := reorder(fs, []string{"repo", "-json", "--", "-weird-dir"})
	want := []string{"-json", "--", "repo", "-weird-dir"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorder = %q, want %q", got, want)
	}
	if err := fs.Parse(got); err != nil || !reflect.DeepEqual(fs.Args(), []string{"repo", "-weird-dir"}) {
		t.Errorf("args = %q, err = %v", fs.Args(), err)
	}
}

func TestScanJSON_NoAPIsFoundIsStillJSON(t *testing.T) {
	var out bytes.Buffer
	if err := runScan([]string{"-json", t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers []any `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Providers == nil {
		t.Errorf("not JSON with a providers list: %v\n%s", err, out.String())
	}
}

func TestRepoPathMistakesAreNamed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(file, []byte("x"), 0o644)
	if err := runScan([]string{"/definitely/not/here"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing dir: %v", err)
	}
	if err := runScan([]string{file}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Errorf("file: %v", err)
	}
	if err := runFix([]string{"-no-tests", "/definitely/not/here"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("fix on a missing dir must fail, got %v", err)
	}
}

func TestDiff_DefaultsTheNameToTheSpecTitleAndPrintsEmptyJSONAsAList(t *testing.T) {
	spec := filepath.Join("..", "..", "internal", "specdiff", "testdata", "acme_before.yaml")
	var out bytes.Buffer
	if err := runDiff([]string{"-json", spec, spec}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Errorf("identical specs: %q, want []", out.String())
	}
	after := filepath.Join("..", "..", "internal", "specdiff", "testdata", "acme_after.yaml")
	out.Reset()
	if err := runDiff([]string{spec, after}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(out.String(), "API:") {
		t.Errorf("the report should be titled with the spec's own title:\n%s", out.String())
	}
}

func TestClipStr_NeverSplitsACharacter(t *testing.T) {
	got := clipStr(strings.Repeat("ü", 60), 52)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 52 {
		t.Errorf("clipStr = %q", got)
	}
}
