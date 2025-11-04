package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Version and BuildData get replaced during build with the commit hash and time of build
var (
	CommitHash = ""
	BuildDate  = ""
)

var (
	appName    = ""
	appVersion = ""
	appURL     = ""
)

func init() {
	if err := godotenv.Load(); err != nil {
		panic(err)
	}

	appName = os.Getenv("APPNAME")
	if appName == "" {
		appName = "nomex"
	}

	appVersion = os.Getenv("APPVERSION")
	if appVersion == "" {
		appVersion = "1.0"
	}

	appURL = os.Getenv("APPURL")
	if appURL == "" {
		panic("APPURL environment variable is not set")
	}
}

func userAgent() string {
	commit := CommitHash
	if commit == "" {
		commit = "unknown"
	}

	built := normalizeBuildDate(BuildDate)

	goVer := sanitizeToken(runtime.Version())
	goOS := sanitizeToken(runtime.GOOS)
	goArch := sanitizeToken(runtime.GOARCH)

	// product/version + comment
	return fmt.Sprintf("%s/%s (+%s; commit=%s; built=%s; go=%s; os=%s; arch=%s)",
		appName, appVersion, appURL, commit, built, goVer, goOS, goArch)
}

func normalizeBuildDate(s string) string {
	if s == "" {
		return "unknown"
	}
	layouts := []string{
		time.RFC3339,
		"2006.01.02.150405",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return "unknown"
}

// RFC 9110 "token" characters are limited so we must scrub spaces/semicolons/parens
// https://www.rfc-editor.org/rfc/rfc9110.html
func sanitizeToken(s string) string {
	s = strings.TrimSpace(s)
	// replace disallowed characters with hyphens to stay parser-friendly
	replacer := strings.NewReplacer(" ", "-", "(", "-", ")", "-", ";", "-", "/", "-", "\\", "-", "\"", "-", "'", "-")
	return replacer.Replace(s)
}
