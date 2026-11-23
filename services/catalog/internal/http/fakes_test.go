package httpapi

import (
	"context"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// fakeBrowse returns a fixed page or error and records the last call.
type fakeBrowse struct {
	page    browse.Page
	err     error
	sort    browse.Sort
	q, user string
	cursor  string
	limit   int
}

func (f *fakeBrowse) Feed(_ context.Context, sort browse.Sort, c string, limit int) (browse.Page, error) {
	f.sort, f.cursor, f.limit = sort, c, limit
	return f.page, f.err
}

func (f *fakeBrowse) Search(_ context.Context, q, c string, limit int) (browse.Page, error) {
	f.q, f.cursor, f.limit = q, c, limit
	return f.page, f.err
}

func (f *fakeBrowse) Creator(_ context.Context, user, c string, limit int) (browse.Page, error) {
	f.user, f.cursor, f.limit = user, c, limit
	return f.page, f.err
}

// fakeStudio returns fixed results and records the last call.
type fakeStudio struct {
	page   browse.Page
	detail video.Detail
	err    error

	sub, id            string
	title, description *string
}

func (f *fakeStudio) List(_ context.Context, sub, _ string, _ int) (browse.Page, error) {
	f.sub = sub
	return f.page, f.err
}

func (f *fakeStudio) Update(_ context.Context, sub, id string, title, description *string) (video.Detail, error) {
	f.sub, f.id, f.title, f.description = sub, id, title, description
	return f.detail, f.err
}

func (f *fakeStudio) Delete(_ context.Context, sub, id string) error {
	f.sub, f.id = sub, id
	return f.err
}

// fakeModeration returns a fixed error and records the last call.
type fakeModeration struct {
	err          error
	operator, id string
	action       string
}

func (f *fakeModeration) Hide(_ context.Context, op, id string) error {
	f.operator, f.id, f.action = op, id, "hide"
	return f.err
}

func (f *fakeModeration) Restore(_ context.Context, op, id string) error {
	f.operator, f.id, f.action = op, id, "restore"
	return f.err
}

// testOptions returns router options with empty fakes for every service.
func testOptions() Options {
	return Options{
		Verifier:   fakeVerifier{},
		Videos:     &fakeVideos{},
		Browse:     &fakeBrowse{},
		Studio:     &fakeStudio{},
		Moderation: &fakeModeration{},
	}
}
