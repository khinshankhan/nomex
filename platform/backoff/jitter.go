package backoff

import (
	"math"
	"math/rand"
	"time"
)

type Strategy interface {
	Next(attempt int) time.Duration
}

// fullJitter implements backoff = min(cap, base * 2^attempt), return U[0, backoff].
type fullJitter struct {
	Base time.Duration
	Cap  time.Duration
	RNG  *rand.Rand
}

// NewFullJitter returns a Strategy using the AWS [Full Jitter] algorithm.
// If `RNG` is nil, a new time-seeded RNG is created.
//
// [Full Jitter]: https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/
func NewFullJitter(cfg fullJitter) Strategy {
	rng := cfg.RNG
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return &fullJitter{
		Base: cfg.Base,
		Cap:  cfg.Cap,
		RNG:  rng,
	}
}

func (f *fullJitter) Next(attempt int) time.Duration {
	// safeguard, should not happen but just in case
	if attempt < 0 || f.Base <= 0 || f.Cap <= 0 {
		return 0
	}

	// TODO: handle overflow?
	pow := math.Pow(2, float64(attempt))
	max := time.Duration(float64(f.Base) * pow)
	if max > f.Cap {
		max = f.Cap
	}

	// safeguard, should not happen but just in case
	if max <= 0 {
		return 0
	}

	// U[0, p]
	return time.Duration(f.RNG.Int63n(int64(max)))
}

type NewJitterConfig = fullJitter

// NewJitter is the default backoff retry strategy using [NewFullJitter].
// https://cloud.google.com/storage/docs/retry-strategy
func NewJitter(cfg NewJitterConfig) Strategy {
	return NewFullJitter(cfg)
}
