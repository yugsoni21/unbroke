package scan

import "regexp"

func compileAnchored(p string) *regexp.Regexp { return regexp.MustCompile(`(?i)^` + p + `$`) }
