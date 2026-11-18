package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoaderDefaultsAndValues(t *testing.T) {
	l := FromMap(map[string]string{
		"NAME":    "auth",
		"PORT":    "9000",
		"DEBUG":   "true",
		"TIMEOUT": "15m",
		"ORIGINS": "a, ,b",
		"BLANK":   "   ",
	})

	if got := l.String("NAME", "x"); got != "auth" {
		t.Errorf("String = %q", got)
	}
	if got := l.String("BLANK", "def"); got != "def" {
		t.Errorf("blank value should fall back to default, got %q", got)
	}
	if got := l.Int("PORT", 1); got != 9000 {
		t.Errorf("Int = %d", got)
	}
	if got := l.Int("MISSING", 7); got != 7 {
		t.Errorf("Int default = %d", got)
	}
	if got := l.Bool("DEBUG", false); !got {
		t.Error("Bool = false")
	}
	if got := l.Duration("TIMEOUT", time.Second); got != 15*time.Minute {
		t.Errorf("Duration = %v", got)
	}
	if got := l.List("ORIGINS", nil); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("List = %v", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoaderCollectsAllErrors(t *testing.T) {
	l := FromMap(map[string]string{
		"PORT":    "eighty",
		"DEBUG":   "maybe",
		"TIMEOUT": "soon",
		"EMPTY":   "",
	})

	l.Required("MISSING")
	l.Required("EMPTY")
	if got := l.Int("PORT", 8000); got != 8000 {
		t.Errorf("invalid Int should return default, got %d", got)
	}
	l.Bool("DEBUG", false)
	l.Duration("TIMEOUT", time.Second)

	err := l.Err()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, key := range []string{"MISSING", "EMPTY", "PORT", "DEBUG", "TIMEOUT"} {
		if !strings.Contains(err.Error(), key+":") {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
