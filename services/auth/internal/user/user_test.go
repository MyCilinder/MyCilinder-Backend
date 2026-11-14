package user

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeDisplayName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		rule string
	}{
		{"Diego Gomez", "Diego Gomez", ""},
		{"  Darío  ", "Darío", ""},
		{strings.Repeat("ñ", 50), strings.Repeat("ñ", 50), ""}, // 50 code points, 100 bytes
		{"", "", RuleRequired},
		{"   ", "", RuleRequired},
		{strings.Repeat("a", 51), "", RuleLength},
		{"bad\x00name", "", RuleFormat},
		{"line\nbreak", "", RuleFormat},
		{"\xff\xfe", "", RuleFormat},
	}
	for _, tt := range tests {
		got, err := NormalizeDisplayName(tt.in)
		if tt.rule == "" {
			if err != nil || got != tt.want {
				t.Errorf("%q: got %q, %v; want %q", tt.in, got, err, tt.want)
			}
			continue
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Rule != tt.rule {
			t.Errorf("%q: got err %v, want rule %s", tt.in, err, tt.rule)
		}
	}
}
