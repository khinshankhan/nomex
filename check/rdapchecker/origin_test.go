package rdapchecker

import (
	"context"
	"testing"
	"time"
)

// Resolution must group by server, not by suffix: .com and .net are one
// Verisign origin with different paths, so they share a rate limit.
func TestOriginGroupsBySever(t *testing.T) {
	if testing.Short() {
		t.Skip("fetches the IANA bootstrap registry")
	}

	r, err := NewOriginResolver()
	if err != nil {
		t.Fatalf("NewOriginResolver: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	com, err := r.Origin(ctx, "example.com")
	if err != nil {
		t.Fatalf("Origin(.com): %v", err)
	}
	net, err := r.Origin(ctx, "example.net")
	if err != nil {
		t.Fatalf("Origin(.net): %v", err)
	}
	dev, err := r.Origin(ctx, "example.dev")
	if err != nil {
		t.Fatalf("Origin(.dev): %v", err)
	}

	if com == "" || dev == "" {
		t.Fatalf("no origin resolved: com=%q dev=%q", com, dev)
	}
	if com != net {
		t.Errorf(".com and .net resolved to %q and %q; expected one Verisign origin", com, net)
	}
	if com == dev {
		t.Errorf(".com and .dev both resolved to %q; expected different registries", com)
	}

	// An unserved suffix is not an error, just no origin.
	none, err := r.Origin(ctx, "example.invalidtldthatdoesnotexist")
	if err != nil {
		t.Errorf("unserved suffix returned an error: %v", err)
	}
	if none != "" {
		t.Errorf("unserved suffix resolved to %q", none)
	}
}
