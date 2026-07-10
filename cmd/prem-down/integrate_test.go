// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"strings"
	"testing"
)

func TestUsageIntegrate(t *testing.T) {
	var b strings.Builder
	usageIntegrate(&b)
	got := b.String()
	for _, want := range []string{"Usage: prem-down integrate", "--remove", "right-click"} {
		if !strings.Contains(got, want) {
			t.Errorf("usageIntegrate() output missing %q:\n%s", want, got)
		}
	}
}
