// Package config reads nomex's environment. These are per-deployment settings
// (database, contact, logging), so they're set once in the environment. Flags
// still override them.
package config

import (
	"os"
	"strings"
)

// Environment variables nomex reads.
const (
	// EnvContact identifies whoever runs this instance to the registries it
	// queries, and is required for anything that queries one.
	//
	// NOTE: there's no default on purpose. A shipped one would point abuse
	// reports at whoever the URL names instead of whoever is actually querying.
	EnvContact = "NOMEX_CONTACT"

	// EnvDB is the database path.
	EnvDB = "NOMEX_DB"

	// EnvLog selects the log format, "json" for JSON and anything else for
	// console.
	EnvLog = "NOMEX_LOG"

	// EnvLogSource includes the calling file and line in each log line.
	EnvLogSource = "NOMEX_LOG_SOURCE"
)

// DefaultDBPath matches the Makefile's DB variable so the CLI and `make
// migrate` use the same database by default.
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
