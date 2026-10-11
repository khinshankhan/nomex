package platform

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// UserAgentOptions is what the build info can't supply.
type UserAgentOptions struct {
	// Name is the product token, eg "nomex". Defaults to info.Name.
	Name string

	// URL is a contact address for registry operators.
	URL string

	// Extra adds key=value pairs, eg service="rdap", sorted.
	Extra map[string]string
}

// UserAgent renders info and opts as an RFC 9110 User-Agent value:
//
//	name/a1b2c3d (+https://github.com/khinshankhan/name; go=go1.26.4; os=linux; arch=amd64)
func UserAgent(info *VersionInfo, opts UserAgentOptions) string {
	name := token(opts.Name, info.Module)
	ver := token(info.Version, "unknown")
	if info.Dirty {
		// so a dev build isn't mistaken for a release
		ver += "-dirty"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s/%s", name, ver)

	sb.WriteString(" (")
	if url := comment(opts.URL); url != "" {
		fmt.Fprintf(&sb, "+%s; ", url)
	}
	fmt.Fprintf(&sb, "go=%s; os=%s; arch=%s",
		comment(runtime.Version()), comment(runtime.GOOS), comment(runtime.GOARCH))

	if date := comment(info.Date); date != "" {
		fmt.Fprintf(&sb, "; built=%s", date)
	}

	keys := make([]string, 0, len(opts.Extra))
	for k := range opts.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "; %s=%s", comment(k), comment(opts.Extra[k]))
	}
	sb.WriteString(")")

	return sb.String()
}

// token scrubs s to RFC 9110 token characters, def when empty. Too strict for
// the comment, which needs "/" and ":".
func token(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}

	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
			return r
		default:
			return '-'
		}
	}, s)
}

// comment scrubs s for the parenthesized comment, dropping parens and
// backslashes.
func comment(s string) string {
	s = strings.TrimSpace(s)

	return strings.Map(func(r rune) rune {
		switch {
		case r == '(', r == ')', r == '\\':
			return '-'
		case r < 0x20 || r == 0x7f:
			// control chars, CR/LF would split the header
			return -1
		default:
			return r
		}
	}, s)
}
