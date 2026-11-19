package httpapi

import (
	"net/http"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// handlers holds the route handlers. Add the services they call (browse, videos,
// playback, ops) as fields here and pass them in through Options.
type handlers struct{}

// GET /feed?sort=recent|most_viewed&cursor=&limit=
func (h *handlers) getFeed(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GET /search?q=&cursor=&limit=
func (h *handlers) searchVideos(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GET /creators/{userId}/videos
func (h *handlers) listCreatorVideos(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GET /videos/{videoId} (optional token)
func (h *handlers) getVideo(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GET /videos/{videoId}/playback (optional token)
func (h *handlers) getPlayback(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// GET /me/videos
func (h *handlers) listMyVideos(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// PATCH /videos/{videoId}
func (h *handlers) updateVideo(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// DELETE /videos/{videoId}
func (h *handlers) deleteVideo(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// POST /ops/videos/{videoId}/hide
func (h *handlers) hideVideo(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

// POST /ops/videos/{videoId}/restore
func (h *handlers) restoreVideo(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

func notImplemented(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, http.StatusNotImplemented, problem.NotImplemented, "")
}
