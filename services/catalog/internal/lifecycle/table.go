// Package lifecycle owns every status change of a video after Upload creates it
// (Architecture §2.3). Changes only move forward: each action has a fixed set of source
// statuses, and the UPDATE is conditional on the current status, so duplicate or
// out-of-order requests change nothing.
package lifecycle

import (
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Action is a lifecycle transition.
type Action string

const (
	Processing Action = "processing" // video.processing event
	Ready      Action = "ready"      // video.ready event
	Fail       Action = "fail"       // video.failed event
	Expire     Action = "expire"     // expiry job: PENDING_UPLOAD older than 24 h
	Timeout    Action = "timeout"    // sweep job: PROCESSING older than 2 h
	Hide       Action = "hide"       // operator
	Restore    Action = "restore"    // operator
	Delete     Action = "delete"     // owner (soft delete, BR-05)
)

// Rule is one row of the transition table.
type Rule struct {
	From []video.Status
	To   video.Status
}

// Allows reports whether the rule accepts a video in status s.
func (r Rule) Allows(s video.Status) bool {
	for _, f := range r.From {
		if f == s {
			return true
		}
	}
	return false
}

// table is the state diagram of Architecture §2.3.
//
// ready and fail are also accepted from PENDING_UPLOAD: SQS standard queues do not
// guarantee order, so a video.ready may arrive before video.processing (Catalog plan,
// decision 1). EXPIRED and DELETED are final and accept nothing.
var table = map[Action]Rule{
	Processing: {From: []video.Status{video.StatusPendingUpload}, To: video.StatusProcessing},
	Ready:      {From: []video.Status{video.StatusPendingUpload, video.StatusProcessing}, To: video.StatusReady},
	Fail:       {From: []video.Status{video.StatusPendingUpload, video.StatusProcessing}, To: video.StatusFailed},
	Expire:     {From: []video.Status{video.StatusPendingUpload}, To: video.StatusExpired},
	Timeout:    {From: []video.Status{video.StatusProcessing}, To: video.StatusFailed},
	Hide:       {From: []video.Status{video.StatusReady}, To: video.StatusHidden},
	Restore:    {From: []video.Status{video.StatusHidden}, To: video.StatusReady},
	Delete:     {From: []video.Status{video.StatusReady, video.StatusHidden, video.StatusFailed}, To: video.StatusDeleted},
}

// RuleFor returns the rule for a, and false for an unknown action.
func RuleFor(a Action) (Rule, bool) {
	r, ok := table[a]
	return r, ok
}

// Actions lists every action, for tests and documentation.
func Actions() []Action {
	return []Action{Processing, Ready, Fail, Expire, Timeout, Hide, Restore, Delete}
}
