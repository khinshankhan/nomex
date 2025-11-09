package fields

import libfields "github.com/khinshankhan/logstox/fields"

type Field = libfields.Field
type FieldKind = libfields.FieldKind

const (
	FieldKindInvalid    = libfields.FieldKindInvalid
	FieldKindAny        = libfields.FieldKindAny
	FieldKindString     = libfields.FieldKindString
	FieldKindBool       = libfields.FieldKindBool
	FieldKindInt64      = libfields.FieldKindInt64
	FieldKindUint64     = libfields.FieldKindUint64
	FieldKindFloat64    = libfields.FieldKindFloat64
	FieldKindTime       = libfields.FieldKindTime
	FieldKindDuration   = libfields.FieldKindDuration
	FieldKindError      = libfields.FieldKindError
	FieldKindStrings    = libfields.FieldKindStrings
	FieldKindBools      = libfields.FieldKindBools
	FieldKindInt64s     = libfields.FieldKindInt64s
	FieldKindUint64s    = libfields.FieldKindUint64s
	FieldKindFloat64s   = libfields.FieldKindFloat64s
	FieldKindErrors     = libfields.FieldKindErrors
	FieldKindDict       = libfields.FieldKindDict
	FieldKindRawJSON    = libfields.FieldKindRawJSON
	FieldKindHexBytes   = libfields.FieldKindHexBytes
	FieldKindLazyFields = libfields.FieldKindLazyFields
	FieldKindLazyValue  = libfields.FieldKindLazyValue
	FieldKindTimestamp  = libfields.FieldKindTimestamp

	ErrorKey     = libfields.ErrorKey
	TimestampKey = libfields.TimestampKey
)

var (
	Any       = libfields.Any
	String    = libfields.String
	Bool      = libfields.Bool
	Int       = libfields.Int
	Int64     = libfields.Int64
	Uint      = libfields.Uint
	Uint64    = libfields.Uint64
	Float64   = libfields.Float64
	TimeField = libfields.TimeField
	Duration  = libfields.Duration

	Error      = libfields.Error
	NamedError = libfields.NamedError

	Strings  = libfields.Strings
	Bools    = libfields.Bools
	Int64s   = libfields.Int64s
	Uint64s  = libfields.Uint64s
	Float64s = libfields.Float64s
	Errors   = libfields.Errors

	Dict        = libfields.Dict
	RawJSON     = libfields.RawJSON
	Hex         = libfields.Hex
	LazyFields  = libfields.LazyFields
	Lazy        = libfields.Lazy
	Timestamp   = libfields.Timestamp
	TimestampAt = libfields.TimestampAt

	From = libfields.From
	Nop  = libfields.Nop
)
