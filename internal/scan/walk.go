// Package scan finds where a codebase calls particular API endpoints.
//
// It reads source text only; it never runs anything from the repository, so
// scanning code you do not trust is safe. Calls are found three ways: a URL
// path written out in the code, a route string such as "GET /repos/{owner}",
// and an SDK call such as stripe.refunds.create(. The first two are precise.
// The third is a heuristic and is reported with lower confidence.
package scan

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".venv": true, "venv": true, "__pycache__": true, ".next": true,
	".terraform": true, ".gradle": true, ".idea": true, ".unbroke": true, "coverage": true,
	"Pods": true, ".tox": true, "site-packages": true,
}

var codeExt = map[string]bool{
	".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rb": true, ".go": true, ".java": true, ".kt": true, ".kts": true,
	".php": true, ".cs": true, ".swift": true, ".rs": true, ".scala": true,
	".ex": true, ".exs": true, ".erl": true, ".dart": true, ".lua": true, ".pl": true,
	".sh": true, ".bash": true, ".vue": true, ".svelte": true, ".erb": true, ".ejs": true,
	".yml": true, ".yaml": true, ".toml": true, ".env": true, ".ini": true, ".cfg": true,
}

// manifests are dependency files, where naming a provider means the project
// uses it.
var manifests = map[string]bool{
	"package.json": true, "requirements.txt": true, "pyproject.toml": true, "setup.py": true,
	"pipfile": true, "go.mod": true, "gemfile": true, "composer.json": true, "cargo.toml": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true, "mix.exs": true,
	"pubspec.yaml": true, "package.swift": true, "podfile": true,
}

// DefaultMaxFileBytes skips minified bundles and generated blobs.
const DefaultMaxFileBytes = 1 << 20

type file struct {
	rel  string
	base string
	data []byte
}

// walk calls fn for every readable source or manifest file under root.
func walk(root string, maxBytes int64, fn func(file)) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFileBytes
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				// An unreadable repository must not read as "no API usage found".
				return err
			}
			return nil // unreadable entries below it are skipped, not fatal
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && d.Name() != ".github")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, sockets and devices are never followed
		}
		base := strings.ToLower(d.Name())
		if !manifests[base] && !codeExt[strings.ToLower(filepath.Ext(base))] {
			return nil
		}
		if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, "-lock.json") || strings.HasSuffix(base, ".lock") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || isBinary(data) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		fn(file{rel: filepath.ToSlash(rel), base: base, data: data})
		return nil
	})
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}
