package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// Recover turns a panic into a 500 problem response and logs the stack trace.
// http.ErrAbortHandler is re-raised, as net/http expects.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			Logger(r.Context()).Error("panic",
				"panic", fmt.Sprint(v),
				"stack", string(debug.Stack()),
			)
			problem.Write(w, r, http.StatusInternalServerError, problem.Internal, "")
		}()
		next.ServeHTTP(w, r)
	})
}
