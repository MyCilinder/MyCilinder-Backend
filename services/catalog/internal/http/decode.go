package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
)

// maxBodyBytes bounds request bodies. The largest Catalog body is a 5,000-character
// description, up to 20 KB in UTF-8 plus JSON escaping.
const maxBodyBytes = 64 << 10

// decode reads a single JSON object into dst. Unknown fields are rejected because every
// request schema has additionalProperties: false. On failure it writes a 400 problem and
// returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil && dec.More() {
		err = errors.New("body must contain a single JSON object")
	}
	if err == nil {
		return true
	}

	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.Is(err, io.EOF):
		writeValidation(w, r, "request body is required")
	case errors.As(err, &maxErr):
		writeValidation(w, r, "request body is too large")
	case errors.As(err, &typeErr):
		writeValidation(w, r, fmt.Sprintf("%s has the wrong type", typeErr.Field),
			problem.FieldError{Field: typeErr.Field, Rule: "type"})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		writeValidation(w, r, "unknown field "+field, problem.FieldError{Field: field, Rule: "unknown-field"})
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		writeValidation(w, r, "request body must be valid JSON")
	default:
		writeValidation(w, r, "request body must be a JSON object")
	}
	return false
}

func writeValidation(w http.ResponseWriter, r *http.Request, detail string, errs ...problem.FieldError) {
	problem.Write(w, r, http.StatusBadRequest, problem.ValidationError, detail, errs...)
}

// pageParams reads cursor and limit. A missing limit is the default (20); a limit that
// is not an integer is reported like an out-of-range one.
func pageParams(w http.ResponseWriter, r *http.Request) (rawCursor string, limit int, ok bool) {
	q := r.URL.Query()
	limit = browse.DefaultLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeValidation(w, r, "limit must be between 1 and 50",
				problem.FieldError{Field: "limit", Rule: "range", Message: "limit must be between 1 and 50"})
			return "", 0, false
		}
		limit = n
	}
	return q.Get("cursor"), limit, true
}
