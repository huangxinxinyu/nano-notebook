package main

import "testing"

func TestParseRebuildConfigSupportsOneTraceOrAll(t *testing.T) {
	one, err := parseRebuildConfig([]string{"trace-1"}, func(string) string { return "postgres://observability" })
	if err != nil || one.TraceID != "trace-1" || one.All {
		t.Fatalf("one = %#v, error=%v", one, err)
	}
	all, err := parseRebuildConfig([]string{"all"}, func(string) string { return "postgres://observability" })
	if err != nil || !all.All {
		t.Fatalf("all = %#v, error=%v", all, err)
	}
}
