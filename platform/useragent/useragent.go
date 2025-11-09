package useragent

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

type AppMeta struct {
	Name    string            // "nomex"
	Version string            // "1.0"
	URL     string            // "https://example.com/nomex"
	Commit  string            // short SHA or full SHA
	Built   time.Time         // time of build
	Extra   map[string]string // optional: service="rdap", etc
}

func Build(m AppMeta) string {
	commit := tok(m.Commit, "unknown")
	built := "unknown"
	if !m.Built.IsZero() {
		built = m.Built.UTC().Format(time.RFC3339)
	}

	sb := strings.Builder{}
	// product / version
	fmt.Fprintf(&sb, "%s/%s", tok(m.Name, "app"), tok(m.Version, "0"))

	// comment
	sb.WriteString(" (+")
	sb.WriteString(tok(m.URL, "n/a"))
	fmt.Fprintf(&sb, "; commit=%s; built=%s; go=%s; os=%s; arch=%s",
		commit, built, tok(runtime.Version(), ""), tok(runtime.GOOS, ""), tok(runtime.GOARCH, ""))

	for k, v := range m.Extra {
		fmt.Fprintf(&sb, "; %s=%s", tok(k, ""), tok(v, ""))
	}
	sb.WriteString(")")
	return sb.String()
}

func tok(s, def string) string {
	if s == "" {
		s = def
	}

	// RFC 9110 "token" characters are limited so we must scrub spaces/semicolons/parens
	// https://www.rfc-editor.org/rfc/rfc9110.html
	s = strings.TrimSpace(s)
	repl := strings.NewReplacer(" ", "-", "(", "-", ")", "-", ";", "-", "/", "-", "\\", "-", "\"", "-", "'", "-")
	return repl.Replace(s)
}
