package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

const (
	maxSingleUpload = 100 << 20 // above this the contract switches to multipart
	partSize        = 64 << 20
	maxUploadBytes  = 1 << 30
)

var uploadTypes = map[string]bool{"video/mp4": true, "video/quicktime": true, "video/webm": true}

type createUploadRequest struct {
	Title           string  `json:"title"`
	Description     *string `json:"description"`
	FileName        string  `json:"fileName"`
	ContentType     string  `json:"contentType"`
	SizeBytes       int64   `json:"sizeBytes"`
	DurationSeconds *int    `json:"durationSeconds"`
}

func limitExceeded(w http.ResponseWriter, r *http.Request, field, rule, msg string) {
	problem.Write(w, r, http.StatusUnprocessableEntity, problem.LimitExceeded, msg,
		problem.FieldError{Field: field, Rule: rule, Message: msg})
}

// instructions builds the SingleUpload or MultipartUpload body. The URLs point at this
// mock (through the gateway) instead of S3; the body is read and discarded.
func (s *server) instructions(r *http.Request, v *Video, onlyParts []int) map[string]any {
	expires := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Second)
	base := s.baseURL(r) + "/mock-s3/raw/" + v.ID + "/source?X-Amz-Expires=900&X-Amz-Signature=mock"
	if !v.Multipart {
		return map[string]any{
			"videoId":   v.ID,
			"method":    "PUT",
			"uploadUrl": base,
			"requiredHeaders": map[string]string{
				"Content-Type":   v.ContentType,
				"Content-Length": strconv.FormatInt(v.SizeBytes, 10),
			},
			"expiresAt": expires,
		}
	}
	numbers := onlyParts
	if len(numbers) == 0 {
		for n := 1; n <= v.Parts; n++ {
			numbers = append(numbers, n)
		}
	}
	parts := make([]map[string]any, 0, len(numbers))
	for _, n := range numbers {
		parts = append(parts, map[string]any{"partNumber": n, "url": fmt.Sprintf("%s&uploadId=mock&partNumber=%d", base, n)})
	}
	return map[string]any{
		"videoId": v.ID, "method": "MULTIPART", "partSizeBytes": partSize, "parts": parts, "expiresAt": expires,
	}
}

func (s *server) createUpload(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		badRequest(w, r, "Idempotency-Key", "required", "Idempotency-Key header is required (1-255 characters)")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		badRequest(w, r, "", "format", "could not read body")
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(raw)))
	var in createUploadRequest
	if !decodeStrict(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if !checkLength(w, r, "title", in.Title, 1, 100) || !checkLength(w, r, "fileName", in.FileName, 1, 255) {
		return
	}
	if in.Description != nil && !checkLength(w, r, "description", *in.Description, 0, 5000) {
		return
	}
	switch {
	case in.SizeBytes < 1:
		badRequest(w, r, "sizeBytes", "range", "sizeBytes must be at least 1")
		return
	case in.DurationSeconds != nil && *in.DurationSeconds < 1:
		badRequest(w, r, "durationSeconds", "range", "durationSeconds must be at least 1")
		return
	case !uploadTypes[in.ContentType]:
		limitExceeded(w, r, "contentType", "unsupported-format", "only MP4, MOV and WebM are accepted")
		return
	case in.SizeBytes > maxUploadBytes:
		limitExceeded(w, r, "sizeBytes", "max-file-size", "the maximum file size is 1 GB")
		return
	case in.DurationSeconds != nil && *in.DurationSeconds > 600:
		limitExceeded(w, r, "durationSeconds", "max-duration", "the maximum duration is 10 minutes")
		return
	}

	me := viewer(r)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])

	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if prev, ok := s.store.idem[me+"|"+key]; ok {
		if prev.bodyHash != hash {
			conflict(w, r, "Idempotency-Key was already used with a different body")
			return
		}
		v := s.store.videos[prev.videoID]
		s.writeUploadCreated(w, r, v)
		return
	}
	desc := ""
	if in.Description != nil {
		desc = *in.Description
	}
	v := &Video{
		ID: newID(), OwnerID: me, Title: in.Title, Description: desc, Status: StatusPendingUpload,
		CreatedAt: time.Now().UTC().Truncate(time.Second), ContentType: in.ContentType, SizeBytes: in.SizeBytes,
	}
	if in.SizeBytes > maxSingleUpload {
		v.Multipart = true
		v.Parts = int((in.SizeBytes + partSize - 1) / partSize)
	}
	s.store.videos[v.ID] = v
	s.store.idem[me+"|"+key] = idemEntry{videoID: v.ID, bodyHash: hash}
	s.writeUploadCreated(w, r, v)
}

func (s *server) writeUploadCreated(w http.ResponseWriter, r *http.Request, v *Video) {
	w.Header().Set("Location", "/api/v1/videos/"+v.ID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, s.instructions(r, v, nil))
}

// uploadOwned loads the caller's own video for the upload routes. Caller holds the lock.
func (s *server) uploadOwned(w http.ResponseWriter, r *http.Request) (*Video, bool) {
	v, ok := s.store.videos[r.PathValue("videoId")]
	switch {
	case !ok || v.Status == StatusDeleted:
		notFound(w, r)
		return nil, false
	case v.OwnerID != viewer(r):
		forbidden(w, r)
		return nil, false
	}
	return v, true
}

func (s *server) refreshUploadURLs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PartNumbers []int `json:"partNumbers"`
	}
	if r.ContentLength != 0 && r.Body != nil {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(strings.TrimSpace(string(raw))) > 0 {
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil {
				badRequest(w, r, "", "format", "request body is not valid JSON")
				return
			}
		}
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.uploadOwned(w, r)
	if !ok {
		return
	}
	if v.Status != StatusPendingUpload {
		conflict(w, r, "video is no longer waiting for an upload")
		return
	}
	for _, n := range body.PartNumbers {
		if !v.Multipart || n < 1 || n > v.Parts {
			badRequest(w, r, "partNumbers", "range", "partNumbers must be parts of this multipart upload")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.instructions(r, v, body.PartNumbers))
}

func (s *server) completeUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Parts []struct {
			PartNumber int    `json:"partNumber"`
			ETag       string `json:"eTag"`
		} `json:"parts"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if len(body.Parts) == 0 {
		badRequest(w, r, "parts", "required", "parts must list every uploaded part")
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.uploadOwned(w, r)
	if !ok {
		return
	}
	if !v.Multipart || v.Status != StatusPendingUpload {
		conflict(w, r, "video is not a pending multipart upload")
		return
	}
	if len(body.Parts) != v.Parts {
		badRequest(w, r, "parts", "format", fmt.Sprintf("expected %d parts, got %d", v.Parts, len(body.Parts)))
		return
	}
	s.startProcessing(r, v)
	w.WriteHeader(http.StatusNoContent)
}

// putObject stands in for the presigned S3 PUT. The bytes are read and thrown away.
func (s *server) putObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("videoId")
	n, err := io.Copy(io.Discard, r.Body)
	if err != nil {
		http.Error(w, "upload interrupted", http.StatusBadRequest)
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.store.videos[id]
	if !ok || v.Status != StatusPendingUpload {
		http.Error(w, "AccessDenied", http.StatusForbidden)
		return
	}
	middleware.Logger(r.Context()).Info("mock upload received and discarded", "videoId", id, "bytes", n,
		"part", r.URL.Query().Get("partNumber"))
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%s-%d", id, r.URL.Query().Get("partNumber"), n)))
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	if r.URL.Query().Get("partNumber") == "" {
		// Single PUT: S3 ObjectCreated would queue the transcode now.
		s.startProcessing(r, v)
	}
	w.WriteHeader(http.StatusOK)
}

// startProcessing moves the video to PROCESSING and, unless PROCESSING_DELAY is 0, to
// READY after the delay, playing one of the stock clips. Caller holds the lock.
func (s *server) startProcessing(r *http.Request, v *Video) {
	v.Status = StatusProcessing
	if s.cfg.ProcessingDelay <= 0 {
		return
	}
	id := v.ID
	time.AfterFunc(s.cfg.ProcessingDelay, func() { s.store.markReady(id) })
	middleware.Logger(r.Context()).Info("mock transcode scheduled", "videoId", id, "readyIn", s.cfg.ProcessingDelay.String())
}
