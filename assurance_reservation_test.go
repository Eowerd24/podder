package main

import (
	"testing"
)

func TestReservationAddressSyntax(t *testing.T) {
	for input, want := range map[string]string{"localhost": "127.0.0.1", " LOCALHOST ": "127.0.0.1", "[::1]": "::1", "0:0:0:0:0:0:0:1": "::1", "": "", "*": "*", "0.0.0.0": "0.0.0.0", "::": "::"} {
		got, err := CanonicalReservationAddress(input)
		if err != nil || got != want {
			t.Fatalf("%q => %q %v", input, got, err)
		}
	}
	for _, input := range []string{"example.invalid", "localhost:80", "127.0.0.1/8", "[127.0.0.1]", "[[::1]]", "fe80::1%eth0", "[::1]:80"} {
		if _, err := CanonicalReservationAddress(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	if !AddressesConflict("localhost", "127.0.0.1") || AddressesConflict("localhost", "::1") || DeclaredEndpointsEquivalent("0.0.0.0", "::") {
		t.Fatal("alias/family distinction lost")
	}
}
