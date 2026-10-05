// Package logx is nomex's logger, logstox as the interface with zap
// underneath. Call sites only see logstox, so the backend can change here.
package logx

import (
	"time"

	"github.com/khinshankhan/logstox"
	"github.com/khinshankhan/logstox/adapter"
	"github.com/khinshankhan/logstox/backend/zapx"
	"github.com/khinshankhan/logstox/fields"

	"github.com/khinshankhan/nomex/platform/config"
)

// Logger is what every call site holds.
type Logger = adapter.Adapter[zapx.ZapField, fields.Field]

// Field is re-exported so callers only need one import.
type Field = fields.Field

// Config describes a logger.
type Config struct {
	// Name tags every line.
	Name string

	// Development selects console output over JSON. It defaults on since a
	// sweep is usually watched in a terminal, NOMEX_LOG=json turns it off.
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
		// skip the adapter frame so the caller is whoever actually logged
		CallerSkip: 2,
	}

	return adapter.Adapter[zapx.ZapField, fields.Field]{
		Base:   backend.New(logstox.Options[zapx.ZapField]{Name: cfg.Name}),
		ToBase: zapx.ToZap,
	}
}

// defaultLogger is built at init, there's nothing to defer.
var defaultLogger = New(Config{
	Name: "nomex",
	// JSON when asked for, console otherwise
	Development: !config.JSONLogs(),
	AddSource:   config.LogSource(),
})

// Default returns the process logger.
func Default() Logger {
	return defaultLogger
}
