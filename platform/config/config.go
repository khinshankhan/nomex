// Package config reads nomex's environment. Flags override it.
package config

import (
	"os"
	"strings"
)

// Environment variables nomex reads.
const (
	// EnvContact identifies whoever runs this instance to the registries it
	// queries. Required for anything that queries one.
	//
	// NOTE: no default, a shipped one would point abuse reports at the wrong
	// person.
	EnvContact = "NOMEX_CONTACT"

	// EnvDB is the database path.
	EnvDB = "NOMEX_DB"

	// EnvLog is "json" for JSON logs, anything else for console.
	EnvLog = "NOMEX_LOG"

	// EnvLogSource adds the calling file and line to each log line.
	EnvLogSource = "NOMEX_LOG_SOURCE"
)

// DefaultDBPath matches the Makefile's DB.
const DefaultDBPath = "./var/nomex.db"

// DB returns the configured database path.
func DB() string {
	if v := strings.TrimSpace(os.Getenv(EnvDB)); v != "" {
		return v
	}
	return DefaultDBPath
}

// Contact returns the configured contact, which may be empty.
func Contact() string {
	return strings.TrimSpace(os.Getenv(EnvContact))
}

// JSONLogs reports whether logs should be JSON.
func JSONLogs() bool {
	return strings.EqualFold(os.Getenv(EnvLog), "json")
}

// LogSource reports whether to include caller information.
func LogSource() bool {
	return truthy(os.Getenv(EnvLogSource))
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
