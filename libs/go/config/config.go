// Package config reads typed settings from environment variables.
//
// A Loader collects every problem it finds, so a service can report all missing or
// invalid variables at once and exit before serving traffic (fail fast).
//
//	env := config.FromEnv()
//	port := env.Int("PORT", 8000)
//	poolID := env.Required("COGNITO_USER_POOL_ID")
//	if err := env.Err(); err != nil { ... }
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader reads values through a lookup function and records errors.
type Loader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// FromEnv returns a Loader backed by the process environment.
func FromEnv() *Loader { return &Loader{lookup: os.LookupEnv} }

// FromMap returns a Loader backed by a map. Intended for tests.
func FromMap(m map[string]string) *Loader {
	return &Loader{lookup: func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}}
}

// raw returns the trimmed value and whether it is set and non-empty.
func (l *Loader) raw(key string) (string, bool) {
	v, ok := l.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

func (l *Loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: "+format, append([]any{key}, args...)...))
}

// String returns the value of key, or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v, ok := l.raw(key); ok {
		return v
	}
	return def
}

// Required returns the value of key and records an error when it is unset or empty.
func (l *Loader) Required(key string) string {
	v, ok := l.raw(key)
	if !ok {
		l.fail(key, "is required")
	}
	return v
}

// Int returns key parsed as an integer, or def when unset.
func (l *Loader) Int(key string, def int) int {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.fail(key, "must be an integer, got %q", v)
		return def
	}
	return n
}

// Bool returns key parsed with strconv.ParseBool, or def when unset.
func (l *Loader) Bool(key string, def bool) bool {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be a boolean, got %q", v)
		return def
	}
	return b
}

// Duration returns key parsed with time.ParseDuration (for example "15m"), or def when unset.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.fail(key, "must be a duration such as 30s or 15m, got %q", v)
		return def
	}
	return d
}

// List returns key split on commas with empty items removed, or def when unset.
func (l *Loader) List(key string, def []string) []string {
	v, ok := l.raw(key)
	if !ok {
		return def
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Err returns every recorded error joined, or nil.
func (l *Loader) Err() error { return errors.Join(l.errs...) }
