package playback

import (
	"context"
	"errors"
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cfsign"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

const (
	owner = "11111111-1111-4111-8111-111111111111"
	other = "22222222-2222-4222-8222-222222222222"
	vid   = "33333333-3333-4333-8333-333333333333"
)

type repo struct{ v video.Video }

func (r repo) GetByID(_ context.Context, id string) (video.Video, error) {
	if id != r.v.ID {
		return video.Video{}, video.ErrNotFound
	}
	return r.v, nil
}

type signer struct{ calls int }

func (s *signer) Sign(id string) (cfsign.Grant, error) {
	s.calls++
	return cfsign.Grant{MasterPlaylistURL: "https://d/videos/" + id + "/master.m3u8"}, nil
}

func TestGrant(t *testing.T) {
	statuses := []video.Status{video.StatusPendingUpload, video.StatusProcessing, video.StatusReady,
		video.StatusFailed, video.StatusHidden, video.StatusDeleted, video.StatusExpired}
	for _, st := range statuses {
		for name, viewer := range map[string]string{"guest": "", "other": other, "owner": owner} {
			sg := &signer{}
			svc := NewService(repo{video.Video{ID: vid, OwnerID: owner, Status: st}}, sg)
			_, err := svc.Grant(context.Background(), vid, viewer)

			var conflict *video.ConflictError
			switch {
			case st == video.StatusReady:
				if err != nil || sg.calls != 1 {
					t.Errorf("%s/%s: err %v calls %d, want signed", st, name, err, sg.calls)
				}
			case viewer == owner && st != video.StatusDeleted:
				if !errors.As(err, &conflict) || conflict.Current != st {
					t.Errorf("%s/%s: err %v, want 409", st, name, err)
				}
			default:
				if !errors.Is(err, video.ErrNotFound) {
					t.Errorf("%s/%s: err %v, want 404", st, name, err)
				}
			}
			if st != video.StatusReady && sg.calls != 0 {
				t.Errorf("%s/%s: signed a non-READY video", st, name)
			}
		}
	}
}

func TestGrantEdgeCases(t *testing.T) {
	svc := NewService(repo{video.Video{ID: vid, Status: video.StatusReady}}, nil)
	if _, err := svc.Grant(context.Background(), vid, ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no signer: %v", err)
	}
	if _, err := svc.Grant(context.Background(), "x", ""); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if _, err := svc.Grant(context.Background(), other, ""); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}
