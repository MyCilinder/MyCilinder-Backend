//go:build integration

package browse_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/testdb"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// These tests run the browse service on the real store (no cache), as catalog_svc.

func service(t *testing.T) *browse.Service {
	return browse.NewService(store.NewVideos(testdb.Service(t), nil), nil)
}

// walk reads every page and returns the ids in order. mid runs once, after page 2.
func walk(t *testing.T, next func(cursor string) (browse.Page, error), mid func()) []string {
	t.Helper()
	var ids []string
	cur := ""
	for page := 1; ; page++ {
		p, err := next(cur)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(p.Items) > 7 {
			t.Fatalf("page %d has %d items", page, len(p.Items))
		}
		for _, v := range p.Items {
			ids = append(ids, v.ID)
		}
		if page == 2 && mid != nil {
			mid()
		}
		if p.NextCursor == "" {
			return ids
		}
		if page > 1000 {
			t.Fatal("pagination does not end")
		}
		cur = p.NextCursor
	}
}

// checkOrder asserts every id in want appears exactly once in got, in the same order,
// and that got has no duplicates.
func checkOrder(t *testing.T, got, want []string) {
	t.Helper()
	seen := map[string]int{}
	for i, id := range got {
		if _, dup := seen[id]; dup {
			t.Fatalf("id %s returned twice", id)
		}
		seen[id] = i
	}
	last := -1
	for _, id := range want {
		i, ok := seen[id]
		if !ok {
			t.Fatalf("id %s missing (gap)", id)
		}
		if i < last {
			t.Fatalf("id %s out of order", id)
		}
		last = i
	}
}

func TestRecentFeedPagination(t *testing.T) {
	f := testdb.New(t)
	svc := service(t)
	ctx := context.Background()

	// 20 READY videos one minute apart, years in the past so other rows don't interleave,
	// with pairs sharing a timestamp so the id tiebreak is exercised.
	base := time.Now().AddDate(-20, 0, 0).Add(-time.Duration(rand.IntN(1e6)) * time.Second)
	var want []string
	type row struct {
		id  string
		pub time.Time
	}
	var rows []row
	for i := 0; i < 20; i++ {
		pub := base.Add(-time.Duration(i/2) * time.Minute)
		rows = append(rows, row{f.Insert(testdb.Video{PublishedAt: &pub}), pub})
	}
	// Expected order: published_at desc, then id desc.
	for i := 0; i < len(rows); i += 2 {
		a, b := rows[i], rows[i+1]
		if a.id < b.id {
			a, b = b, a
		}
		want = append(want, a.id, b.id)
	}
	// Non-READY and NULL-published rows never appear.
	hidden := f.Insert(testdb.Video{Status: "HIDDEN"})
	pending := f.Insert(testdb.Video{Status: "PENDING_UPLOAD"})

	var late string
	got := walk(t, func(c string) (browse.Page, error) { return svc.Feed(ctx, browse.SortRecent, c, 7) },
		func() { late = f.Insert(testdb.Video{}) }) // newest video, arrives mid-walk

	checkOrder(t, got, want)
	for _, id := range []string{hidden, pending, late} {
		for _, g := range got {
			if g == id {
				t.Errorf("id %s should not appear", id)
			}
		}
	}
}

func TestMostViewedFeedPagination(t *testing.T) {
	f := testdb.New(t)
	svc := service(t)
	ctx := context.Background()

	// View counts far above anything else in the database, with ties.
	top := int64(9e15) + rand.Int64N(1e12)
	type row struct {
		id    string
		views int64
	}
	var rows []row
	for i := 0; i < 15; i++ {
		id := f.Insert(testdb.Video{})
		views := top - int64(i/3)
		f.Stats(id, views, 0, 0)
		rows = append(rows, row{id, views})
	}
	var want []string
	for i := 0; i < len(rows); i += 3 {
		group := []string{rows[i].id, rows[i+1].id, rows[i+2].id}
		// Same view count: id descending.
		for a := 0; a < 3; a++ {
			for b := a + 1; b < 3; b++ {
				if group[b] > group[a] {
					group[a], group[b] = group[b], group[a]
				}
			}
		}
		want = append(want, group...)
	}

	got := walk(t, func(c string) (browse.Page, error) { return svc.Feed(ctx, browse.SortMostViewed, c, 7) },
		func() { // a new row lands at the top mid-walk
			id := f.Insert(testdb.Video{})
			f.Stats(id, top+1, 0, 0)
		})
	if len(got) < len(want) || strings.Join(got[:len(want)], ",") != strings.Join(want, ",") {
		t.Fatalf("most_viewed order wrong:\n got  %v\n want %v", got[:min(len(got), len(want))], want)
	}
	checkOrder(t, got, want)
}

func TestCreatorPages(t *testing.T) {
	f := testdb.New(t)
	svc := service(t)
	ctx := context.Background()

	owner := f.User("Creadora")
	var want []string
	for i := 0; i < 9; i++ {
		pub := time.Now().Add(-time.Duration(i) * time.Hour)
		want = append(want, f.Insert(testdb.Video{Owner: owner, PublishedAt: &pub}))
	}
	f.Insert(testdb.Video{Owner: owner, Status: "FAILED"})
	f.Insert(testdb.Video{}) // someone else's

	got := walk(t, func(c string) (browse.Page, error) { return svc.Creator(ctx, owner, c, 7) }, nil)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("creator page:\n got  %v\n want %v", got, want)
	}

	// No profile: 404, even if the user has videos.
	ghost := testdb.UUID()
	f.Insert(testdb.Video{Owner: ghost})
	if _, err := svc.Creator(ctx, ghost, "", 20); err != video.ErrNotFound {
		t.Errorf("creator without profile: err = %v, want ErrNotFound", err)
	}
}

// token returns a random word that the Spanish stemmer leaves alone, so each test
// searches only its own rows.
func token() string {
	const letters = "bcdfghjklmnpqrstvwxz"
	b := make([]byte, 10)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}

func TestSearchStemmingAndWeights(t *testing.T) {
	f := testdb.New(t)
	svc := service(t)
	ctx := context.Background()
	tok := token()

	inTitle := f.Insert(testdb.Video{Title: "Recorrido " + tok, Description: "algo distinto"})
	inDesc := f.Insert(testdb.Video{Title: "Otra cosa", Description: "Un recorrido largo " + tok})
	f.Insert(testdb.Video{Title: "Recorrido " + tok, Status: "HIDDEN"})

	// Plural and upper case still match the singular (Spanish stemming).
	p, err := svc.Search(ctx, "RECORRIDOS "+tok, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.Items[0].ID != inTitle || p.Items[1].ID != inDesc {
		ids := []string{}
		for _, v := range p.Items {
			ids = append(ids, v.ID)
		}
		t.Fatalf("results %v, want title match %s then description match %s", ids, inTitle, inDesc)
	}

	// Query syntax is treated as plain words, not as tsquery operators: punctuation is
	// dropped, and every remaining word must match (plainto_tsquery ANDs them).
	for _, q := range []string{tok + " & | ! ( ) :* <->", "'" + tok + "'", "!" + tok} {
		p, err := svc.Search(ctx, q, "", 20)
		if err != nil {
			t.Fatalf("q=%q: %v", q, err)
		}
		if len(p.Items) == 0 {
			t.Errorf("q=%q: no results", q)
		}
	}
	// "or" is a word here, not an operator, so it must match too and nothing does.
	if p, err := svc.Search(ctx, tok+" OR 1=1; --", "", 20); err != nil || len(p.Items) != 0 {
		t.Errorf("OR treated as an operator: %d items, err %v", len(p.Items), err)
	}

	// No match and stop words only: 200 with no items.
	for _, q := range []string{token(), "el la de"} {
		p, err := svc.Search(ctx, q, "", 20)
		if err != nil || len(p.Items) != 0 || p.NextCursor != "" {
			t.Errorf("q=%q: %d items, next %q, err %v", q, len(p.Items), p.NextCursor, err)
		}
	}
}

func TestSearchPagination(t *testing.T) {
	f := testdb.New(t)
	svc := service(t)
	ctx := context.Background()
	tok := token()

	// Different repetition counts give different ranks; several share a rank.
	all := map[string]bool{}
	for i := 0; i < 17; i++ {
		desc := strings.Repeat(tok+" ", i%4)
		all[f.Insert(testdb.Video{Title: "Video " + tok, Description: desc})] = true
	}
	got := walk(t, func(c string) (browse.Page, error) { return svc.Search(ctx, tok, c, 5) }, nil)
	if len(got) != len(all) {
		t.Fatalf("got %d results, want %d", len(got), len(all))
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] || !all[id] {
			t.Fatalf("duplicate or foreign id %s", id)
		}
		seen[id] = true
	}
}
