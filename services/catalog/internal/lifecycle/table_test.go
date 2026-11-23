package lifecycle

import (
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// TestTable checks every (status, action) pair against the state diagram of
// Architecture §2.3, plus decision 1 of the Catalog plan (ready and fail accepted from
// PENDING_UPLOAD).
func TestTable(t *testing.T) {
	const (
		P = video.StatusPendingUpload
		R = video.StatusProcessing
		Y = video.StatusReady
		F = video.StatusFailed
		H = video.StatusHidden
		D = video.StatusDeleted
		E = video.StatusExpired
	)
	allowed := map[Action]map[video.Status]video.Status{
		Processing: {P: R},
		Ready:      {P: Y, R: Y},
		Fail:       {P: F, R: F},
		Expire:     {P: E},
		Timeout:    {R: F},
		Hide:       {Y: H},
		Restore:    {H: Y},
		Delete:     {Y: D, H: D, F: D},
	}
	statuses := []video.Status{P, R, Y, F, H, D, E}

	if len(Actions()) != len(allowed) {
		t.Fatalf("Actions() has %d entries, test covers %d", len(Actions()), len(allowed))
	}
	for _, a := range Actions() {
		rule, ok := RuleFor(a)
		if !ok {
			t.Fatalf("no rule for %s", a)
		}
		for _, s := range statuses {
			want, ok := allowed[a][s]
			if rule.Allows(s) != ok {
				t.Errorf("%s from %s: allowed = %v, want %v", a, s, rule.Allows(s), ok)
			}
			if ok && rule.To != want {
				t.Errorf("%s from %s: to = %s, want %s", a, s, rule.To, want)
			}
		}
	}
	// Final statuses accept nothing.
	for _, a := range Actions() {
		rule, _ := RuleFor(a)
		if rule.Allows(D) || rule.Allows(E) {
			t.Errorf("%s accepts a final status", a)
		}
	}
	if _, ok := RuleFor("publish"); ok {
		t.Error("unknown action has a rule")
	}
}
