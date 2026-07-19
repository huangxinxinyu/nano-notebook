package main

import "testing"

func TestParseGrantConfigBoundsActionAndCapability(t *testing.T) {
	config, err := parseGrantConfig([]string{"grant", "Operator@Example.com", "platform.trace.read"}, func(key string) string {
		if key == "NANO_DATABASE_URL" {
			return "postgres://application"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseURL != "postgres://application" || config.Email != "operator@example.com" || config.Action != "grant" {
		t.Fatalf("config = %#v", config)
	}
	if _, err := parseGrantConfig([]string{"grant", "operator@example.com", "platform.admin"}, func(string) string { return "" }); err == nil {
		t.Fatal("accepted unknown platform capability")
	}
}
