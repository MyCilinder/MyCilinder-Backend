package cursor

import (
	"encoding/base64"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

const id = "8d0c5e2a-3b4f-4a8e-9c1d-2f6b7a8e9d01"

func TestRoundTrip(t *testing.T) {
	ts := time.Date(2026, 11, 22, 19, 25, 30, 123456000, time.UTC) // microseconds, like Postgres
	tests := []Cursor{
		AfterTime(Recent, "", ts, id),
		AfterTime(Creator, "11111111-1111-4111-8111-111111111111", ts, id),
		AfterTime(Mine, "", ts, id),
		AfterInt(MostViewed, "", 0, id),
		AfterInt(MostViewed, "", math.MaxInt64, id),
		AfterFloat(Search, SearchScope("recorrido campus"), 0.0607927, id),
		AfterFloat(Search, SearchScope("x"), 1e-20, id),
	}
	for _, c := range tests {
		raw := c.Encode()
		if strings.ContainsAny(raw, "+/=") {
			t.Errorf("%s: not base64url without padding: %s", c.Sort, raw)
		}
		got, err := Decode(raw, c.Sort, c.Scope)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.Sort, err)
		}
		if got.ID != c.ID || !got.Time.Equal(c.Time) || got.Int != c.Int || got.Float != c.Float {
			t.Errorf("%s: round trip %+v != %+v", c.Sort, got, c)
		}
	}
}

// The float key must survive exactly, or the keyset comparison would skip or repeat rows.
func TestFloatExact(t *testing.T) {
	for _, f := range []float32{0.1, 0.0607927, 0.09910322, math.SmallestNonzeroFloat32, math.MaxFloat32} {
		got, err := Decode(AfterFloat(Search, "", f, id).Encode(), Search, "")
		if err != nil || got.Float != f {
			t.Errorf("%v: got %v err %v", f, got.Float, err)
		}
	}
}

func TestTampered(t *testing.T) {
	good := AfterTime(Recent, "", time.Now(), id).Encode()
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	bad := map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("a", MaxLength+1),
		"not base64":     "!!!",
		"padded base64":  base64.URLEncoding.EncodeToString([]byte(`{"s":"recent"}`)) + "=",
		"not json":       enc("hello"),
		"bad id":         enc(`{"s":"recent","k":"2026-01-01T00:00:00Z","id":"x"}`),
		"bad time":       enc(`{"s":"recent","k":"yesterday","id":"` + id + `"}`),
		"missing key":    enc(`{"s":"recent","id":"` + id + `"}`),
		"truncated":      good[:len(good)-4],
		"sql in key":     enc(`{"s":"most_viewed","k":"1; DROP TABLE x","id":"` + id + `"}`),
		"negative views": enc(`{"s":"most_viewed","k":"-1","id":"` + id + `"}`),
	}
	for name, raw := range bad {
		sort := Recent
		if strings.Contains(name, "views") || strings.Contains(name, "sql") {
			sort = MostViewed
		}
		if _, err := Decode(raw, sort, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestWrongListRejected(t *testing.T) {
	recent := AfterTime(Recent, "", time.Now(), id).Encode()
	if _, err := Decode(recent, MostViewed, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("recent cursor accepted for most_viewed")
	}
	creatorA := AfterTime(Creator, "a", time.Now(), id).Encode()
	if _, err := Decode(creatorA, Creator, "b"); !errors.Is(err, ErrInvalid) {
		t.Errorf("cursor of creator a accepted for creator b")
	}
	search := AfterFloat(Search, SearchScope("campus"), 0.1, id).Encode()
	if _, err := Decode(search, Search, SearchScope("tutorial")); !errors.Is(err, ErrInvalid) {
		t.Errorf("cursor of another search accepted")
	}
}
