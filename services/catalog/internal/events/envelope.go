// Package events consumes video-status-queue (Interface Design §2.1): the Transcoder's
// video.processing, video.ready, and video.failed events, applied through the lifecycle.
//
// Delivery is at-least-once and unordered, so handling is idempotent: an event that does
// not move the video forward is acknowledged and changes nothing (REL-14).
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Event types.
const (
	TypeProcessing = "video.processing"
	TypeReady      = "video.ready"
	TypeFailed     = "video.failed"
)

// MaxReasonLength bounds the failure reason shown to the creator (FR-CR-02).
const MaxReasonLength = 500

// ErrInvalid marks a message that cannot be parsed or validated. It is left on the queue,
// so after the redrive limit it reaches the DLQ, where an operator can inspect it.
var ErrInvalid = errors.New("events: invalid message")

// Envelope is the event body (Interface Design §2.1).
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	VideoID    string          `json:"videoId"`
	OccurredAt time.Time       `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

// ReadyData is the data of video.ready.
type ReadyData struct {
	DurationSeconds int      `json:"durationSeconds"`
	Renditions      []string `json:"renditions"`
}

// FailedData is the data of video.failed.
type FailedData struct {
	Reason string `json:"reason"`
}

// snsNotification is the wrapper SNS adds when the subscription does not use raw message
// delivery. The design enables raw delivery, but accepting both keeps the consumer
// working if the subscription setting drifts.
type snsNotification struct {
	Type     string `json:"Type"`
	Message  string `json:"Message"`
	TopicArn string `json:"TopicArn"`
}

// Event is a parsed and validated envelope.
type Event struct {
	Envelope
	Ready  ReadyData
	Failed FailedData
}

// Parse decodes and validates a message body, raw or wrapped by SNS.
func Parse(body string) (Event, error) {
	var probe snsNotification
	if err := json.Unmarshal([]byte(body), &probe); err == nil && probe.Type == "Notification" && probe.TopicArn != "" {
		body = probe.Message
	}

	var e Event
	dec := json.NewDecoder(strings.NewReader(body))
	if err := dec.Decode(&e.Envelope); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !video.ValidID(e.VideoID) {
		return Event{}, fmt.Errorf("%w: videoId %q is not a UUID", ErrInvalid, e.VideoID)
	}
	e.VideoID = strings.ToLower(e.VideoID)

	data := e.Data
	if len(data) == 0 || string(data) == "null" {
		data = json.RawMessage("{}")
	}
	switch e.Type {
	case TypeProcessing:
	case TypeReady:
		if err := json.Unmarshal(data, &e.Ready); err != nil {
			return Event{}, fmt.Errorf("%w: data: %v", ErrInvalid, err)
		}
		if e.Ready.DurationSeconds <= 0 {
			return Event{}, fmt.Errorf("%w: durationSeconds must be positive", ErrInvalid)
		}
		if len(e.Ready.Renditions) == 0 {
			return Event{}, fmt.Errorf("%w: renditions must not be empty", ErrInvalid)
		}
		seen := map[string]bool{}
		for _, r := range e.Ready.Renditions {
			if r != "360p" && r != "720p" {
				return Event{}, fmt.Errorf("%w: unknown rendition %q", ErrInvalid, r)
			}
			if seen[r] {
				return Event{}, fmt.Errorf("%w: duplicate rendition %q", ErrInvalid, r)
			}
			seen[r] = true
		}
	case TypeFailed:
		if err := json.Unmarshal(data, &e.Failed); err != nil {
			return Event{}, fmt.Errorf("%w: data: %v", ErrInvalid, err)
		}
		e.Failed.Reason = strings.TrimSpace(e.Failed.Reason)
		if e.Failed.Reason == "" || !utf8.ValidString(e.Failed.Reason) {
			return Event{}, fmt.Errorf("%w: reason is required", ErrInvalid)
		}
		if r := []rune(e.Failed.Reason); len(r) > MaxReasonLength {
			e.Failed.Reason = string(r[:MaxReasonLength])
		}
	default:
		return Event{}, fmt.Errorf("%w: unknown type %q", ErrInvalid, e.Type)
	}
	return e, nil
}
