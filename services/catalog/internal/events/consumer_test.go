package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

const vid = "8d0c5e2a-3b4f-4a8e-9c1d-2f6b7a8e9d01"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func env(typ, data string) string {
	return `{"eventId":"e1","type":"` + typ + `","videoId":"` + vid + `","occurredAt":"2026-10-31T20:00:00Z","data":` + data + `}`
}

func TestParse(t *testing.T) {
	e, err := Parse(env(TypeReady, `{"durationSeconds":312,"renditions":["360p","720p"]}`))
	if err != nil || e.Ready.DurationSeconds != 312 || len(e.Ready.Renditions) != 2 || e.OccurredAt.IsZero() {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := Parse(env(TypeProcessing, `{}`)); err != nil || e.Type != TypeProcessing {
		t.Fatalf("processing: %v", err)
	}
	if e, err := Parse(`{"type":"video.processing","videoId":"` + strings.ToUpper(vid) + `"}`); err != nil || e.VideoID != vid {
		t.Fatalf("missing data / upper-case id: %+v %v", e, err)
	}
	e, err = Parse(env(TypeFailed, `{"reason":"  `+strings.Repeat("x", 600)+`  "}`))
	if err != nil || len(e.Failed.Reason) != MaxReasonLength {
		t.Fatalf("failed: len %d err %v", len(e.Failed.Reason), err)
	}

	// SNS-wrapped (raw delivery off) gives the same event.
	wrapped, _ := json.Marshal(map[string]string{
		"Type": "Notification", "MessageId": "m", "TopicArn": "arn:aws:sns:us-east-1:1:video-events",
		"Message": env(TypeReady, `{"durationSeconds":5,"renditions":["360p"]}`),
	})
	if e, err := Parse(string(wrapped)); err != nil || e.Ready.DurationSeconds != 5 {
		t.Fatalf("wrapped: %+v %v", e, err)
	}

	bad := []string{
		"", "not json", `[]`,
		env("video.deleted", `{}`),
		`{"type":"video.processing","videoId":"nope"}`,
		env(TypeReady, `{"durationSeconds":0,"renditions":["360p"]}`),
		env(TypeReady, `{"durationSeconds":5,"renditions":[]}`),
		env(TypeReady, `{"durationSeconds":5,"renditions":["1080p"]}`),
		env(TypeReady, `{"durationSeconds":5,"renditions":["360p","360p"]}`),
		env(TypeReady, `{"durationSeconds":"5","renditions":["360p"]}`),
		env(TypeFailed, `{"reason":"   "}`),
		env(TypeFailed, `{}`),
	}
	for _, b := range bad {
		if _, err := Parse(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%.60q) err = %v, want ErrInvalid", b, err)
		}
	}
}

// fakeMachine returns a fixed error and records calls.
type fakeMachine struct {
	mu    sync.Mutex
	err   error
	calls []lifecycle.Action
	ch    lifecycle.Changes
}

func (f *fakeMachine) Transition(_ context.Context, id string, a lifecycle.Action, ch lifecycle.Changes, _ func(context.Context, pgx.Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, a)
	f.ch = ch
	return f.err
}

// fakeSQS serves batches, then blocks until the context ends.
type fakeSQS struct {
	mu        sync.Mutex
	batches   [][]types.Message
	recvErr   []error
	deleted   []string
	deleteErr error
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	if len(f.recvErr) > 0 {
		err := f.recvErr[0]
		f.recvErr = f.recvErr[1:]
		f.mu.Unlock()
		return nil, err
	}
	if len(f.batches) > 0 {
		b := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		return &sqs.ReceiveMessageOutput{Messages: b}, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

func msg(handle, body string) types.Message {
	return types.Message{MessageId: aws.String(handle), ReceiptHandle: aws.String(handle), Body: aws.String(body)}
}

func TestOutcomes(t *testing.T) {
	ready := env(TypeReady, `{"durationSeconds":30,"renditions":["360p","720p"]}`)
	tests := []struct {
		name       string
		body       string
		machineErr error
		want       Outcome
		deleted    bool
	}{
		{"applied", ready, nil, Applied, true},
		{"duplicate", ready, lifecycle.ErrNoop, Ignored, true},
		{"late event", env(TypeProcessing, `{}`), &video.ConflictError{Current: video.StatusReady}, Ignored, true},
		{"unknown video", ready, video.ErrNotFound, Ignored, true},
		{"database error", ready, errors.New("connection reset"), Retry, false},
		{"unparseable", "{oops", nil, Rejected, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &fakeSQS{}
			m := &fakeMachine{err: tt.machineErr}
			c := NewConsumer(q, "url", m, quiet())
			if got := c.ProcessMessage(context.Background(), msg("h", tt.body)); got != tt.want {
				t.Errorf("outcome = %s, want %s", got, tt.want)
			}
			if (len(q.deleted) == 1) != tt.deleted {
				t.Errorf("deleted = %v, want %v", q.deleted, tt.deleted)
			}
			if tt.want == Rejected && len(m.calls) != 0 {
				t.Errorf("lifecycle called for an unparseable message")
			}
		})
	}
}

func TestEventToAction(t *testing.T) {
	m := &fakeMachine{}
	Handle(context.Background(), m, quiet(), env(TypeReady, `{"durationSeconds":30,"renditions":["720p"]}`))
	if m.calls[0] != lifecycle.Ready || *m.ch.DurationSeconds != 30 || m.ch.Renditions[0] != "720p" {
		t.Errorf("ready: %v %+v", m.calls, m.ch)
	}
	Handle(context.Background(), m, quiet(), env(TypeFailed, `{"reason":"unsupported codec"}`))
	if m.calls[1] != lifecycle.Fail || m.ch.FailureReason != "unsupported codec" {
		t.Errorf("failed: %v %+v", m.calls, m.ch)
	}
	Handle(context.Background(), m, quiet(), env(TypeProcessing, `{}`))
	if m.calls[2] != lifecycle.Processing {
		t.Errorf("processing: %v", m.calls)
	}
}

func TestRunProcessesBatchesAndStops(t *testing.T) {
	ready := env(TypeReady, `{"durationSeconds":30,"renditions":["360p"]}`)
	q := &fakeSQS{
		recvErr: []error{errors.New("throttled")}, // one failed receive, then recovery
		batches: [][]types.Message{
			{msg("a", ready), msg("b", "{bad")},
			{msg("c", env(TypeProcessing, `{}`))},
		},
	}
	m := &fakeMachine{}
	c := NewConsumer(q, "url", m, quiet())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		q.mu.Lock()
		n := len(q.deleted)
		q.mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if strings.Join(sorted(q.deleted), ",") != "a,c" {
		t.Errorf("deleted = %v, want a and c (b is left for the DLQ)", q.deleted)
	}
}

func TestDeleteFailureIsNotFatal(t *testing.T) {
	q := &fakeSQS{deleteErr: errors.New("expired receipt handle")}
	c := NewConsumer(q, "url", &fakeMachine{}, quiet())
	if got := c.ProcessMessage(context.Background(), msg("h", env(TypeProcessing, `{}`))); got != Applied {
		t.Errorf("outcome = %s", got)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
