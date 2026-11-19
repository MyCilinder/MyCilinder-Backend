package main

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

type commentBody struct {
	ID        string    `json:"id"`
	VideoID   string    `json:"videoId"`
	Author    userRef   `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type reportBody struct {
	ID         string     `json:"id"`
	ReporterID string     `json:"reporterId"`
	TargetType string     `json:"targetType"`
	TargetID   string     `json:"targetId"`
	Reason     string     `json:"reason"`
	Details    *string    `json:"details"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	ResolvedBy *string    `json:"resolvedBy"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func toReport(rp *Report) reportBody {
	return reportBody{
		ID: rp.ID, ReporterID: rp.ReporterID, TargetType: rp.TargetType, TargetID: rp.TargetID,
		Reason: rp.Reason, Details: rp.Details, Status: rp.Status, CreatedAt: rp.CreatedAt,
		ResolvedAt: rp.ResolvedAt, ResolvedBy: rp.ResolvedBy,
	}
}

// readyVideo returns the path's video if it is READY, or writes 404. Caller holds the lock.
func (s *server) readyVideo(w http.ResponseWriter, r *http.Request) (*Video, bool) {
	v, ok := s.store.videos[r.PathValue("videoId")]
	if !ok || v.Status != StatusReady {
		notFound(w, r)
		return nil, false
	}
	return v, true
}

func (s *server) getStats(w http.ResponseWriter, r *http.Request) {
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.store.videos[r.PathValue("videoId")]
	if !ok || !visibleTo(v, me) {
		notFound(w, r)
		return
	}
	body := map[string]any{
		"videoId":      v.ID,
		"viewCount":    v.BaseViews,
		"likeCount":    s.store.likeCount(v.ID),
		"commentCount": s.store.commentCount(v.ID),
	}
	if me != "" {
		body["likedByMe"] = s.store.likes[v.ID][me]
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) listComments(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.readyVideo(w, r)
	if !ok {
		return
	}
	var visible []*Comment
	for _, c := range s.store.comments[v.ID] {
		if !c.Hidden {
			visible = append(visible, c)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].CreatedAt.After(visible[j].CreatedAt) })
	items, next, ok := paginate(w, r, visible)
	if !ok {
		return
	}
	out := make([]commentBody, len(items))
	for i, c := range items {
		u := s.store.user(c.AuthorID)
		out[i] = commentBody{ID: c.ID, VideoID: c.VideoID, Author: userRef{u.ID, u.DisplayName}, Body: c.Body, CreatedAt: c.CreatedAt}
	}
	writeJSON(w, http.StatusOK, pageBody[commentBody]{Items: out, NextCursor: next})
}

func (s *server) createComment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Body)
	if !checkLength(w, r, "body", text, 1, 1000) {
		return
	}
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.readyVideo(w, r)
	if !ok {
		return
	}
	c := &Comment{ID: newID(), VideoID: v.ID, AuthorID: me, Body: text, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	s.store.comments[v.ID] = append(s.store.comments[v.ID], c)
	s.store.byID[c.ID] = c
	u := s.store.user(me)
	writeJSON(w, http.StatusCreated, commentBody{ID: c.ID, VideoID: v.ID, Author: userRef{u.ID, u.DisplayName}, Body: c.Body, CreatedAt: c.CreatedAt})
}

func (s *server) setLike(w http.ResponseWriter, r *http.Request, liked bool) {
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.readyVideo(w, r)
	if !ok {
		return
	}
	if s.store.likes[v.ID] == nil {
		s.store.likes[v.ID] = map[string]bool{}
	}
	if liked {
		s.store.likes[v.ID][me] = true
	} else {
		delete(s.store.likes[v.ID], me)
	}
	writeJSON(w, http.StatusOK, map[string]any{"liked": liked, "likeCount": s.store.likeCount(v.ID)})
}

func (s *server) likeVideo(w http.ResponseWriter, r *http.Request)   { s.setLike(w, r, true) }
func (s *server) unlikeVideo(w http.ResponseWriter, r *http.Request) { s.setLike(w, r, false) }

// recordView counts at most one view per viewer and video every 30 minutes (BR-VIEW).
func (s *server) recordView(w http.ResponseWriter, r *http.Request) {
	who := viewer(r)
	if who == "" {
		sid := r.Header.Get("X-Session-Id")
		if !uuidRE.MatchString(sid) {
			badRequest(w, r, "X-Session-Id", "session-id-required", "guests must send X-Session-Id (a UUID)")
			return
		}
		who = "session:" + sid
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.readyVideo(w, r)
	if !ok {
		return
	}
	key := v.ID + "|" + who
	if last, seen := s.store.views[key]; !seen || time.Since(last) > 30*time.Minute {
		s.store.views[key] = time.Now()
		v.BaseViews++
	}
	w.WriteHeader(http.StatusAccepted)
}

var reportReasons = map[string]bool{"SPAM": true, "HARASSMENT": true, "SEXUAL_CONTENT": true, "VIOLENCE": true, "COPYRIGHT": true, "OTHER": true}

func (s *server) createReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TargetType string  `json:"targetType"`
		TargetID   string  `json:"targetId"`
		Reason     string  `json:"reason"`
		Details    *string `json:"details"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	switch {
	case body.TargetType != "VIDEO" && body.TargetType != "COMMENT":
		badRequest(w, r, "targetType", "enum", "targetType must be VIDEO or COMMENT")
		return
	case !uuidRE.MatchString(body.TargetID):
		badRequest(w, r, "targetId", "format", "targetId must be a UUID")
		return
	case !reportReasons[body.Reason]:
		badRequest(w, r, "reason", "enum", "reason is not a valid report reason")
		return
	case body.Details != nil && !checkLength(w, r, "details", *body.Details, 0, 1000):
		return
	}

	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	exists := false
	if body.TargetType == "VIDEO" {
		v, ok := s.store.videos[body.TargetID]
		exists = ok && v.Status == StatusReady
	} else if c, ok := s.store.byID[body.TargetID]; ok && !c.Hidden {
		v := s.store.videos[c.VideoID]
		exists = v != nil && v.Status == StatusReady
	}
	if !exists {
		notFound(w, r)
		return
	}
	for _, rp := range s.store.reports {
		if rp.ReporterID == me && rp.TargetID == body.TargetID && rp.Status == "OPEN" {
			conflict(w, r, "you already have an open report for this target")
			return
		}
	}
	rp := &Report{
		ID: newID(), ReporterID: me, TargetType: body.TargetType, TargetID: body.TargetID,
		Reason: body.Reason, Details: body.Details, Status: "OPEN", CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	s.store.reports = append(s.store.reports, rp)
	writeJSON(w, http.StatusCreated, toReport(rp))
}

func (s *server) listReports(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "OPEN"
	}
	if status != "OPEN" && status != "RESOLVED" && status != "DISMISSED" {
		badRequest(w, r, "status", "enum", "status must be OPEN, RESOLVED or DISMISSED")
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	var matching []*Report // s.store.reports is already oldest first
	for _, rp := range s.store.reports {
		if rp.Status == status {
			matching = append(matching, rp)
		}
	}
	items, next, ok := paginate(w, r, matching)
	if !ok {
		return
	}
	out := make([]reportBody, len(items))
	for i, rp := range items {
		out[i] = toReport(rp)
	}
	writeJSON(w, http.StatusOK, pageBody[reportBody]{Items: out, NextCursor: next})
}

func (s *server) resolveReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Status != "RESOLVED" && body.Status != "DISMISSED" {
		badRequest(w, r, "status", "enum", "status must be RESOLVED or DISMISSED")
		return
	}
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	for _, rp := range s.store.reports {
		if rp.ID != r.PathValue("reportId") {
			continue
		}
		if rp.Status != "OPEN" {
			conflict(w, r, "report is already "+rp.Status)
			return
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		rp.Status, rp.ResolvedAt, rp.ResolvedBy = body.Status, &now, &me
		writeJSON(w, http.StatusOK, toReport(rp))
		return
	}
	notFound(w, r)
}

func (s *server) hideComment(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	c, ok := s.store.byID[r.PathValue("commentId")]
	if !ok {
		notFound(w, r)
		return
	}
	c.Hidden = true
	w.WriteHeader(http.StatusNoContent)
}
