package main

import (
	"reflect"
	"testing"
)

func TestInactiveSlotSelection(t *testing.T) {
	tests := []struct {
		active string
		want   string
		port   int
	}{
		{active: "none", want: "blue", port: defaultBluePort},
		{active: "blue", want: "green", port: defaultGreenPort},
		{active: "green", want: "blue", port: defaultBluePort},
	}

	for _, test := range tests {
		gotSlot, gotPort := inactiveSlot(test.active)
		if gotSlot != test.want || gotPort != test.port {
			t.Errorf("inactiveSlot(%q)=(%q, %d), want (%q, %d)", test.active, gotSlot, gotPort, test.want, test.port)
		}
	}
}

func TestHostAliasValidation(t *testing.T) {
	valid := []string{"production-arm", "vps-prod", "100.64.0.1", "my.host"}
	for _, alias := range valid {
		if !hostAliasPattern.MatchString(alias) {
			t.Errorf("host alias %q should be accepted", alias)
		}
	}

	invalid := []string{"", "two words", "user@host", "host;rm -rf /"}
	for _, alias := range invalid {
		if hostAliasPattern.MatchString(alias) {
			t.Errorf("host alias %q should be rejected", alias)
		}
	}
}

func TestShellQuoteArgs(t *testing.T) {
	arguments := []string{"docker", "run", "it's-safe"}
	want := []string{"'docker'", "'run'", "'it'\\''s-safe'"}
	if got := shellQuoteArgs(arguments); !reflect.DeepEqual(got, want) {
		t.Fatalf("shellQuoteArgs()=%q, want %q", got, want)
	}
}
