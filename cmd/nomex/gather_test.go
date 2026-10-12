package main

import (
	"slices"
	"testing"
	"time"
)

func TestCheckEstimates(t *testing.T) {
	const (
		verisign = "https://rdap.verisign.com"
		google   = "https://pubapi.registry.google"
	)
	origins := map[string]string{"com": verisign, "net": verisign, "dev": google}
	originOf := func(tld string) string {
		return origins[tld]
	}

	tests := []struct {
		name string
		tlds []string
		want []registryEstimate
	}{
		{
			// .com and .net share Verisign's 10/s
			name: "shared registry",
			tlds: []string{"com", "net"},
			want: []registryEstimate{{verisign, []string{"com", "net"}, 10, 200 * time.Second}},
		},
		{
			name: "registries in first-seen order",
			tlds: []string{"dev", "com"},
			want: []registryEstimate{
				{google, []string{"dev"}, 1, 1000 * time.Second},
				{verisign, []string{"com"}, 10, 100 * time.Second},
			},
		},
		{
			name: "unknown registry gets the default rate",
			tlds: []string{"zz"},
			want: []registryEstimate{{"", []string{"zz"}, 1, 1000 * time.Second}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkEstimates(1000, tt.tlds, originOf)
			if !slices.EqualFunc(got, tt.want, func(a, b registryEstimate) bool {
				return a.origin == b.origin && slices.Equal(a.tlds, b.tlds) && a.rate == b.rate && a.took == b.took
			}) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
