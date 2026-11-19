package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, r *http.Request, field, rule, msg string) {
	problem.Write(w, r, http.StatusBadRequest, problem.ValidationError, msg,
		problem.FieldError{Field: field, Rule: rule, Message: msg})
}

func notFound(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, http.StatusNotFound, problem.NotFound, "")
}

func conflict(w http.ResponseWriter, r *http.Request, detail string) {
	problem.Write(w, r, http.StatusConflict, problem.Conflict, detail)
}

func forbidden(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, http.StatusForbidden, problem.Forbidden, "")
}

// decodeStrict reads a JSON body and rejects unknown fields, like the real services.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		msg := "request body is not valid JSON"
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			msg = strings.TrimPrefix(err.Error(), "json: ")
		} else if errors.Is(err, io.EOF) {
			msg = "request body is required"
		}
		badRequest(w, r, "", "format", msg)
		return false
	}
	return true
}

// checkLength validates a trimmed or raw string by rune count.
func checkLength(w http.ResponseWriter, r *http.Request, field, value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	if n < min || n > max {
		badRequest(w, r, field, "length", field+" must be "+strconv.Itoa(min)+"-"+strconv.Itoa(max)+" characters")
		return false
	}
	return true
}

// paginate applies limit (1-50, default 20) and an opaque offset cursor. The real
// services use keyset cursors; clients only see an opaque string either way.
func paginate[T any](w http.ResponseWriter, r *http.Request, all []T) ([]T, *string, bool) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			badRequest(w, r, "limit", "range", "limit must be an integer between 1 and 50")
			return nil, nil, false
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		n, err2 := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if err != nil || err2 != nil || !strings.HasPrefix(string(b), "o:") || n < 0 {
			badRequest(w, r, "cursor", "format", "cursor is malformed")
			return nil, nil, false
		}
		offset = n
	}
	if offset > len(all) {
		offset = len(all)
	}
	end := offset + limit
	var next *string
	if end < len(all) {
		c := base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(end)))
		next = &c
	} else {
		end = len(all)
	}
	page := all[offset:end]
	if page == nil {
		page = []T{}
	}
	return page, next, true
}

type pageBody[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// baseURL is where the client reaches this mock: the gateway's public host when proxied
// (X-Forwarded-Host, so it works for localhost:8000 and the emulator's 10.0.2.2:8000),
// or MEDIA_BASE_URL when set.
func (s *server) baseURL(r *http.Request) string {
	if s.cfg.MediaBaseURL != "" {
		return strings.TrimRight(s.cfg.MediaBaseURL, "/")
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		proto = "http"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

var accentFolder = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

func normalize(s string) string { return accentFolder.Replace(strings.ToLower(s)) }

var stopwords = map[string]bool{
	"de": true, "la": true, "el": true, "en": true, "y": true, "a": true, "los": true, "las": true,
	"del": true, "un": true, "una": true, "por": true, "con": true, "para": true, "al": true, "tu": true,
}

// searchTerms turns q into lowercase, accent-free stems, roughly like
// plainto_tsquery('spanish', q): punctuation is ignored and stopwords are dropped.
func searchTerms(q string) []string {
	words := strings.FieldsFunc(normalize(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	var out []string
	for _, w := range words {
		if stopwords[w] {
			continue
		}
		switch {
		case len(w) > 4 && strings.HasSuffix(w, "es"):
			w = w[:len(w)-2]
		case len(w) > 3 && strings.HasSuffix(w, "s"):
			w = w[:len(w)-1]
		}
		out = append(out, w)
	}
	return out
}
