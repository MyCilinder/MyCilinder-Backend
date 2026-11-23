package video

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const (
	ownerID = "11111111-1111-4111-8111-111111111111"
	otherID = "22222222-2222-4222-8222-222222222222"
	videoID = "33333333-3333-4333-8333-333333333333"
)

type fakeRepo struct {
	video Video
	err   error
	calls int
}

func (f *fakeRepo) GetByID(_ context.Context, id string) (Video, error) {
	f.calls++
	if f.err != nil {
		return Video{}, f.err
	}
	if id != f.video.ID {
		return Video{}, ErrNotFound
	}
	return f.video, nil
}

func TestGetVisibility(t *testing.T) {
	statuses := []Status{
		StatusPendingUpload, StatusProcessing, StatusReady, StatusFailed,
		StatusHidden, StatusDeleted, StatusExpired,
	}
	viewers := map[string]string{"guest": "", "other user": otherID, "owner": ownerID}

	for _, st := range statuses {
		for name, viewer := range viewers {
			t.Run(string(st)+"/"+name, func(t *testing.T) {
				repo := &fakeRepo{video: Video{ID: videoID, OwnerID: ownerID, Status: st}}
				got, err := NewService(repo).Get(context.Background(), videoID, viewer)

				isOwner := viewer == ownerID
				wantVisible := st == StatusReady || (isOwner && st != StatusDeleted)
				if !wantVisible {
					if !errors.Is(err, ErrNotFound) {
						t.Fatalf("err = %v, want ErrNotFound", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("err = %v, want visible", err)
				}
				if got.Owner != isOwner {
					t.Errorf("Owner = %v, want %v", got.Owner, isOwner)
				}
				if got.Status != st {
					t.Errorf("Status = %s, want %s", got.Status, st)
				}
			})
		}
	}
}

func TestGetOwnerMatchIgnoresCase(t *testing.T) {
	repo := &fakeRepo{video: Video{ID: videoID, OwnerID: ownerID, Status: StatusFailed}}
	got, err := NewService(repo).Get(context.Background(), videoID, strings.ToUpper(ownerID))
	if err != nil || !got.Owner {
		t.Fatalf("got Owner=%v err=%v, want owner view", got.Owner, err)
	}
}

func TestGetMalformedIDSkipsRepository(t *testing.T) {
	for _, id := range []string{"", "x", "not-a-uuid", videoID + "0", strings.Replace(videoID, "-", "_", 1), "zzzzzzzz-3333-4333-8333-333333333333"} {
		repo := &fakeRepo{video: Video{ID: videoID, Status: StatusReady}}
		_, err := NewService(repo).Get(context.Background(), id, "")
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("id %q: err = %v, want ErrNotFound", id, err)
		}
		if repo.calls != 0 {
			t.Errorf("id %q: repository called %d times", id, repo.calls)
		}
	}
}

func TestGetUnknownAndUnavailable(t *testing.T) {
	svc := NewService(&fakeRepo{video: Video{ID: otherID, Status: StatusReady}})
	if _, err := svc.Get(context.Background(), videoID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	svc = NewService(&fakeRepo{err: errors.New("connection refused")})
	if _, err := svc.Get(context.Background(), videoID, ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("db error: err = %v, want ErrUnavailable", err)
	}
}

func TestHasMedia(t *testing.T) {
	for _, st := range []Status{StatusPendingUpload, StatusProcessing, StatusFailed, StatusHidden, StatusDeleted, StatusExpired} {
		if (Video{Status: st}).HasMedia() {
			t.Errorf("%s: HasMedia = true", st)
		}
	}
	if !(Video{Status: StatusReady}).HasMedia() {
		t.Error("READY: HasMedia = false")
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		in, want, rule string
	}{
		{"  Recorrido por el campus  ", "Recorrido por el campus", ""},
		{"Canción ñandú", "Canción ñandú", ""},
		{strings.Repeat("á", 100), strings.Repeat("á", 100), ""}, // 100 runes, 200 bytes
		{"", "", RuleRequired},
		{"   \t ", "", RuleRequired},
		{strings.Repeat("a", 101), "", RuleLength},
		{"line\nbreak", "", RuleFormat},
		{"bad \xff utf8", "", RuleFormat},
	}
	for _, tt := range tests {
		got, err := NormalizeTitle(tt.in)
		if tt.rule == "" {
			if err != nil || got != tt.want {
				t.Errorf("NormalizeTitle(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
			continue
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Rule != tt.rule || ve.Field != "title" {
			t.Errorf("NormalizeTitle(%q) err = %v, want rule %s", tt.in, err, tt.rule)
		}
	}
}

func TestValidateDescription(t *testing.T) {
	ok := []string{"", "Un recorrido.\n\nCon saltos\tde línea.", strings.Repeat("é", 5000)}
	for _, s := range ok {
		if err := ValidateDescription(s); err != nil {
			t.Errorf("ValidateDescription(len %d) = %v", len(s), err)
		}
	}
	bad := map[string]string{
		strings.Repeat("a", 5001): RuleLength,
		"bell \a":                 RuleFormat,
		"\xff":                    RuleFormat,
	}
	for s, rule := range bad {
		var ve *ValidationError
		if err := ValidateDescription(s); !errors.As(err, &ve) || ve.Rule != rule {
			t.Errorf("ValidateDescription(%.10q) err = %v, want rule %s", s, err, rule)
		}
	}
}
