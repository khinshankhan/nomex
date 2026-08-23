package platform

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// UserAgentOptions carries what the build identity cannot supply on its own.
type UserAgentOptions struct {
	// Name is the product token, e.g. "name". Defaults to info.Name.
	Name string

	// URL is a contact address. The only field a registry operator can act
	// on, so worth setting even when everything else is unknown.
	URL string

	// Extra carries key=value pairs, e.g. service="rdap". Sorted, so the
	// header is stable across runs.
	Extra map[string]string
}

// UserAgent renders info and opts as an RFC 9110 User-Agent value.
//
// The result is one product token followed by a parenthesized comment:
//
//	name/a1b2c3d (+https://github.com/khinshankhan/name; go=go1.26.4; os=linux; arch=amd64)
func UserAgent(info *VersionInfo, opts UserAgentOptions) string {
	name := token(opts.Name, info.Module)
	ver := token(info.Version, "unknown")
	if info.Dirty {
		// So a dev build is not mistaken for a release in someone's logs.
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

// token scrubs s to RFC 9110 token characters, def when empty. Product and
// version only -- too aggressive for the comment, where "/" and ":" are legal.
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

// comment scrubs s for the parenthesized comment. RFC 9110 allows anything
// there but parens and backslashes. Narrower than token so a URL survives.
func comment(s string) string {
	s = strings.TrimSpace(s)

	return strings.Map(func(r rune) rune {
		switch {
		case r == '(', r == ')', r == '\\':
			return '-'
		case r < 0x20 || r == 0x7f:
			// Control chars, incl. the CR/LF that would split the header.
			return -1
		default:
			return r
		}
	}, s)
}
