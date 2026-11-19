package main

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// Video statuses (openapi VideoStatus).
const (
	StatusPendingUpload = "PENDING_UPLOAD"
	StatusProcessing    = "PROCESSING"
	StatusReady         = "READY"
	StatusFailed        = "FAILED"
	StatusHidden        = "HIDDEN"
	StatusDeleted       = "DELETED"
	StatusExpired       = "EXPIRED"
)

type User struct {
	ID          string
	DisplayName string
}

type Video struct {
	ID            string
	OwnerID       string
	Title         string
	Description   string
	Status        string
	FailureReason *string
	CreatedAt     time.Time
	PublishedAt   *time.Time

	// Seeded counters; real likes and comments made during the demo are added on top.
	BaseViews int64
	BaseLikes int64

	// Upload bookkeeping.
	ContentType string
	SizeBytes   int64
	Multipart   bool
	Parts       int
}

type Comment struct {
	ID        string
	VideoID   string
	AuthorID  string
	Body      string
	CreatedAt time.Time
	Hidden    bool
}

type Report struct {
	ID         string
	ReporterID string
	TargetType string
	TargetID   string
	Reason     string
	Details    *string
	Status     string
	CreatedAt  time.Time
	ResolvedAt *time.Time
	ResolvedBy *string
}

type idemEntry struct {
	videoID  string
	bodyHash string
}

// Store keeps all mock state in memory, guarded by one mutex. It resets on restart.
type Store struct {
	mu sync.Mutex

	users    map[string]*User
	videos   map[string]*Video
	comments map[string][]*Comment // by video id, oldest first
	byID     map[string]*Comment
	likes    map[string]map[string]bool // video id -> user id set
	views    map[string]time.Time       // video id + viewer -> last counted view
	reports  []*Report
	idem     map[string]idemEntry // user id + idempotency key
	seeded   map[string]bool      // users that got their starter videos
}

func NewStore() *Store {
	s := &Store{
		users:    map[string]*User{},
		videos:   map[string]*Video{},
		comments: map[string][]*Comment{},
		byID:     map[string]*Comment{},
		likes:    map[string]map[string]bool{},
		views:    map[string]time.Time{},
		idem:     map[string]idemEntry{},
		seeded:   map[string]bool{},
	}
	seed(s, time.Now().UTC())
	return s
}

// visibleTo applies the Catalog visibility rule: everyone sees READY, the owner also sees
// every status except DELETED.
func visibleTo(v *Video, viewer string) bool {
	if v.Status == StatusReady {
		return true
	}
	return viewer != "" && viewer == v.OwnerID && v.Status != StatusDeleted
}

func (s *Store) likeCount(videoID string) int64 {
	return s.videos[videoID].BaseLikes + int64(len(s.likes[videoID]))
}

func (s *Store) commentCount(videoID string) int64 {
	var n int64
	for _, c := range s.comments[videoID] {
		if !c.Hidden {
			n++
		}
	}
	return n
}

func (s *Store) user(id string) User {
	if u, ok := s.users[id]; ok {
		return *u
	}
	return User{ID: id, DisplayName: "Usuario"}
}

// readyVideos returns READY videos matching keep, sorted by less.
func (s *Store) readyVideos(keep func(*Video) bool, less func(a, b *Video) bool) []*Video {
	var out []*Video
	for _, v := range s.videos {
		if v.Status == StatusReady && (keep == nil || keep(v)) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func byRecent(a, b *Video) bool {
	if !a.PublishedAt.Equal(*b.PublishedAt) {
		return a.PublishedAt.After(*b.PublishedAt)
	}
	return a.ID > b.ID
}

func byCreatedDesc(a, b *Video) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// markReady finishes the fake transcode of an uploaded video.
func (s *Store) markReady(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.videos[id]
	if !ok || v.Status != StatusProcessing {
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	v.Status = StatusReady
	if v.PublishedAt == nil {
		v.PublishedAt = &now
	}
}

// newID returns a random UUID v4.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b[:])
}

// stableID returns a name-based UUID (v5 layout), so seeded ids survive restarts.
func stableID(name string) string {
	h := sha1.Sum([]byte("mycilinder-mock:" + name))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b)
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// pick maps an id to one of n media files, the same one every time.
func pick(id string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % uint32(n))
}
