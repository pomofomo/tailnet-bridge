package dnsutil

import (
	"strings"
	"testing"
)

func TestValidateBridgeDomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // empty = expect nil
	}{
		{"valid 4 labels", "smith.ts.example.com", ""},
		{"valid 5 labels", "smith.ts.example.co.uk", ""},
		{"missing ts", "smith.example.com", "ts"},
		{"only 3 labels", "ts.example.com", "4 labels"},
		{"only 2 labels", "example.com", "4 labels"},
		{"uppercase folded", "Smith.ts.Example.com", ""},
		{"double dot", "smith..ts.example.com", "valid DNS name"},
		{"leading dash", "-smith.ts.example.com", "valid DNS name"},
		{"trailing dot", "smith.ts.example.com.", "valid DNS name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBridgeDomain(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestBaseDomain(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"smith.ts.example.com", "ts.example.com"},
		{"SMITH.ts.example.com", "ts.example.com"},
		{"example.com", "com"},
		{"single", "single"},
	} {
		if got := BaseDomain(tc.in); got != tc.want {
			t.Errorf("BaseDomain(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
