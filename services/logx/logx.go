package logx

import (
	"sync"
	"time"

	"github.com/khinshankhan/logstox"
	"github.com/khinshankhan/logstox/adapter"
	"github.com/khinshankhan/logstox/backend/zapx"
	"github.com/khinshankhan/logstox/fields"
)

type Logger = adapter.Adapter[zapx.ZapField, fields.Field]

func newLogger() Logger {
	backend := zapx.Backend{
		Development: true,             // TODO: false for production
		TimeLayout:  time.RFC3339Nano, // optional: override timestamp format
		AddSource:   true,             // optional: include caller info, helpful for debugging for now
		CallerSkip:  2,                // we want to skip adapter site since it's kind of useless
	}
	zl := backend.New(
		logstox.Options[zapx.ZapField]{
			Name: "test",
		},
	)
	logger := adapter.Adapter[zapx.ZapField, fields.Field]{
		Base:   zl,
		ToBase: zapx.ToZap,
	}
	return logger
}

var defaultLogger = sync.OnceValue(newLogger)

func GetDefaultLogger() Logger {
	return defaultLogger()
}
