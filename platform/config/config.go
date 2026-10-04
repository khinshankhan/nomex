// Package config reads nomex's environment.
//
// Everything here is deployment identity rather than a per-invocation choice --
// which database, who to contact, how to log -- so it belongs in the
// environment where it can be set once. Flags still override, because a flag is
// an explicit instruction and an environment variable is a default.
package config

import (
	"os"
	"strings"
)

// Environment variables nomex reads.
const (
	// EnvContact identifies whoever is running this instance to the registries
	// it queries.
	//
	// Required for anything that queries a registry. There is deliberately no
	// default: a shipped one would attribute a stranger's traffic to whoever
	// the URL names, so an operator receiving abuse could not reach the person
	// actually sending it.
	EnvContact = "NOMEX_CONTACT"

	// EnvDB is the database path.
	EnvDB = "NOMEX_DB"

	// EnvLog selects the log format: "json" for machine-readable, anything
	// else for console.
	EnvLog = "NOMEX_LOG"

	// EnvLogSource includes the calling file and line in each log line.
	EnvLogSource = "NOMEX_LOG_SOURCE"
)

// DefaultDBPath matches the Makefile's DB variable, so the CLI and `make
// migrate` operate on the same database without configuration.
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

// JSONLogs reports whether logs should be machine-readable.
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
