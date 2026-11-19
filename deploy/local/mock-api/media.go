package main

import (
	"bufio"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Clip is one MP4 in the media directory.
type Clip struct {
	File            string
	DurationSeconds int
}

// Media holds the stock clips and thumbnails baked into the image (see Dockerfile).
// Every video maps to one clip and one thumbnail by a hash of its id.
type Media struct {
	dir    string
	clips  []Clip
	thumbs []string
}

// LoadMedia reads <dir>/clips.txt ("file seconds" per line) and <dir>/thumbs/*.jpg.
// A missing directory is not fatal: the API still works, media URLs return 404.
func LoadMedia(dir string) *Media {
	m := &Media{dir: dir}
	f, err := os.Open(filepath.Join(dir, "clips.txt"))
	if err != nil {
		slog.Warn("no media clips; playback URLs will 404", "dir", dir, "err", err)
	} else {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) != 2 {
				continue
			}
			d, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			m.clips = append(m.clips, Clip{File: fields[0], DurationSeconds: d})
		}
		_ = f.Close()
	}
	m.thumbs, _ = filepath.Glob(filepath.Join(dir, "thumbs", "*.jpg"))
	sort.Strings(m.thumbs)
	slog.Info("media loaded", "clips", len(m.clips), "thumbnails", len(m.thumbs))
	return m
}

func (m *Media) clip(videoID string) Clip {
	if len(m.clips) == 0 {
		return Clip{DurationSeconds: 10}
	}
	return m.clips[pick(videoID, len(m.clips))]
}

func (m *Media) serveVideo(w http.ResponseWriter, r *http.Request, videoID string) {
	c := m.clip(videoID)
	if c.File == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// ServeFile handles Range requests, which video players need for seeking.
	http.ServeFile(w, r, filepath.Join(m.dir, c.File))
}

func (m *Media) serveThumb(w http.ResponseWriter, r *http.Request, videoID string) {
	if len(m.thumbs) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeFile(w, r, m.thumbs[pick(videoID, len(m.thumbs))])
}
