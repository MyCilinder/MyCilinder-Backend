package cache

import (
	"context"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// VideoTTL is the lifetime of a video:{id} entry (Data Design §3.2).
const VideoTTL = 60 * time.Second

// VideoKey is the detail key of a video.
func VideoKey(id string) string { return "video:" + id }

// Pages implements browse.PageCache.
type Pages struct{ c *Cache }

// NewPages returns a browse.PageCache on c.
func NewPages(c *Cache) Pages { return Pages{c: c} }

// Page caches a whole page. Every page is cached, including empty ones.
func (p Pages) Page(ctx context.Context, key string, ttl time.Duration,
	load func(context.Context) (browse.Page, error)) (browse.Page, error) {
	return Load(ctx, p.c, key, ttl, func(ctx context.Context) (browse.Page, bool, error) {
		pg, err := load(ctx)
		return pg, true, err
	})
}

// Videos wraps a video.Repository with the video:{id} cache. Only READY videos are cached,
// so an owner always reads the current status of a video that is not public, and a status
// change away from READY is visible as soon as Invalidate runs.
type Videos struct {
	c    *Cache
	repo video.Repository
}

// NewVideos returns the cached repository.
func NewVideos(c *Cache, repo video.Repository) *Videos { return &Videos{c: c, repo: repo} }

// GetByID implements video.Repository.
func (v *Videos) GetByID(ctx context.Context, id string) (video.Video, error) {
	return Load(ctx, v.c, VideoKey(id), VideoTTL, func(ctx context.Context) (video.Video, bool, error) {
		got, err := v.repo.GetByID(ctx, id)
		if err != nil {
			return video.Video{}, false, err
		}
		return got, got.Status == video.StatusReady, nil
	})
}

// Invalidate deletes the detail entry. Lifecycle and studio call it after every committed
// write to the video.
func (v *Videos) Invalidate(ctx context.Context, id string) {
	v.c.Delete(ctx, VideoKey(id))
}
