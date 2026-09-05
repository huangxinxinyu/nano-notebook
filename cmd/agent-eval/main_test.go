package main

import "testing"

func TestAgentEvalDefaultsToCurrentAgentRelease(t *testing.T) {
	if defaultAgentRelease != "nano.default@26" {
		t.Fatalf("default Agent release=%q", defaultAgentRelease)
	}
}
