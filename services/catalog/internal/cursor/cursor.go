// Package cursor encodes the opaque keyset cursors used by every list endpoint (Data
// Design §2.2, SCAL-06).
//
// A cursor is base64url (no padding) JSON: {"s": sort, "k": key, "id": last id, "c":
// scope}. The key is the last row's sort value, stored as a string so each sort can use
// its own type:
//
//	recent, creator  published_at (RFC 3339 with nanoseconds)
//	mine             created_at   (RFC 3339 with nanoseconds)
//	most_viewed      view count   (int64)
//	search           rank         (float32, the exact value PostgreSQL returned)
//
// The scope binds a cursor to the list it came from (the creator id, or a hash of the
// search text), so a cursor from another list is rejected instead of returning a
// confusing page.
package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// Sort identifies the list a cursor belongs to.
type Sort string

const (
	Recent     Sort = "recent"
	MostViewed Sort = "most_viewed"
	Search     Sort = "search"
	Creator    Sort = "creator"
	Mine       Sort = "mine"
)

// MaxLength is the longest cursor the API accepts (openapi parameters.Cursor).
const MaxLength = 512

// ErrInvalid is returned for a cursor that is malformed, tampered with, or from another
// list. Callers answer 400.
var ErrInvalid = errors.New("cursor: invalid")

// Cursor is a decoded position: the row after which the next page starts.
type Cursor struct {
	Sort  Sort
	ID    string
	Scope string

	// Exactly one key is meaningful, depending on Sort.
	Time  time.Time
	Int   int64
	Float float32
}

type wire struct {
	S  Sort   `json:"s"`
	K  string `json:"k"`
	ID string `json:"id"`
	C  string `json:"c,omitempty"`
}

// AfterTime returns the cursor for a list sorted by a timestamp.
func AfterTime(s Sort, scope string, t time.Time, id string) Cursor {
	return Cursor{Sort: s, Scope: scope, Time: t, ID: id}
}

// AfterInt returns the cursor for a list sorted by an integer.
func AfterInt(s Sort, scope string, n int64, id string) Cursor {
	return Cursor{Sort: s, Scope: scope, Int: n, ID: id}
}

// AfterFloat returns the cursor for a list sorted by a float4 value.
func AfterFloat(s Sort, scope string, f float32, id string) Cursor {
	return Cursor{Sort: s, Scope: scope, Float: f, ID: id}
}

// Encode returns the opaque string for c.
func (c Cursor) Encode() string {
	w := wire{S: c.Sort, ID: c.ID, C: c.Scope}
	switch keyKind(c.Sort) {
	case kindTime:
		w.K = c.Time.UTC().Format(time.RFC3339Nano)
	case kindInt:
		w.K = strconv.FormatInt(c.Int, 10)
	case kindFloat:
		w.K = strconv.FormatFloat(float64(c.Float), 'g', -1, 32)
	}
	b, _ := json.Marshal(w)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses raw and checks that it belongs to the list identified by sort and scope.
func Decode(raw string, sort Sort, scope string) (Cursor, error) {
	if raw == "" || len(raw) > MaxLength {
		return Cursor{}, ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, ErrInvalid
	}
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return Cursor{}, ErrInvalid
	}
	if w.S != sort || w.C != scope || !validUUID(w.ID) {
		return Cursor{}, ErrInvalid
	}
	c := Cursor{Sort: w.S, ID: w.ID, Scope: w.C}
	switch keyKind(sort) {
	case kindTime:
		c.Time, err = time.Parse(time.RFC3339Nano, w.K)
	case kindInt:
		c.Int, err = strconv.ParseInt(w.K, 10, 64)
		if err == nil && c.Int < 0 {
			err = ErrInvalid
		}
	case kindFloat:
		var f float64
		f, err = strconv.ParseFloat(w.K, 32)
		c.Float = float32(f)
	default:
		err = ErrInvalid
	}
	if err != nil {
		return Cursor{}, ErrInvalid
	}
	return c, nil
}

// SearchScope returns the scope for a search: a short hash of the normalized text, so the
// query itself is not repeated inside the cursor.
func SearchScope(q string) string {
	sum := sha256.Sum256([]byte(q))
	return hex.EncodeToString(sum[:8])
}

type kind int

const (
	kindNone kind = iota
	kindTime
	kindInt
	kindFloat
)

func keyKind(s Sort) kind {
	switch s {
	case Recent, Creator, Mine:
		return kindTime
	case MostViewed:
		return kindInt
	case Search:
		return kindFloat
	}
	return kindNone
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}
