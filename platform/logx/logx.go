// Package logx is nomex's logger: logstox as the interface, zap underneath.
//
// The indirection is the point. Call sites depend on logstox's Field type, so
// the backend is a decision made here rather than one spread across every
// package that logs.
package logx

import (
	"os"
	"strings"
	"time"

	"github.com/khinshankhan/logstox"
	"github.com/khinshankhan/logstox/adapter"
	"github.com/khinshankhan/logstox/backend/zapx"
	"github.com/khinshankhan/logstox/fields"
)

// Logger is what every call site holds.
type Logger = adapter.Adapter[zapx.ZapField, fields.Field]

// Field is re-exported so callers import one package rather than two.
type Field = fields.Field

// Config describes a logger.
type Config struct {
	// Name tags every line, so output from several subsystems in one process
	// stays attributable.
	Name string

	// Development selects human-readable console output over JSON. A sweep
	// runs for days and is usually watched in a terminal, so it defaults on;
	// set NOMEX_LOG=json for machine-readable output instead.
	Development bool

	// AddSource includes the calling file and line.
	AddSource bool
}

// New builds a logger.
func New(cfg Config) Logger {
	backend := zapx.Backend{
		Development: cfg.Development,
		TimeLayout:  time.RFC3339Nano,
		AddSource:   cfg.AddSource,
		// The adapter sits between the call site and zap, so without this the
		// reported caller is the adapter rather than whoever logged.
		CallerSkip: 2,
	}

	return adapter.Adapter[zapx.ZapField, fields.Field]{
		Base:   backend.New(logstox.Options[zapx.ZapField]{Name: cfg.Name}),
		ToBase: zapx.ToZap,
	}
}

// defaultLogger is built at init: there is nothing to defer, and it saves an
// atomic load on every call.
var defaultLogger = New(Config{
	Name: "nomex",
	// JSON when asked for, console otherwise. A long sweep is normally watched
	// by a person.
	Development: !strings.EqualFold(os.Getenv("NOMEX_LOG"), "json"),
	AddSource:   strings.EqualFold(os.Getenv("NOMEX_LOG_SOURCE"), "true"),
})

// Default returns the process logger.
func Default() Logger {
	return defaultLogger
}
