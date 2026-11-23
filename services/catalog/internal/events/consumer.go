package events

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// SQS is the subset of the SQS client the consumer uses. *sqs.Client implements it.
type SQS interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// Transitioner is implemented by *lifecycle.Machine.
type Transitioner interface {
	Transition(ctx context.Context, id string, a lifecycle.Action, ch lifecycle.Changes,
		within func(ctx context.Context, tx pgx.Tx) error) error
}

// Outcome is what happened to one message.
type Outcome string

const (
	Applied  Outcome = "applied"  // status changed; message deleted
	Ignored  Outcome = "ignored"  // duplicate, late, or unknown video; message deleted
	Retry    Outcome = "retry"    // database error; message left for redelivery
	Rejected Outcome = "rejected" // unparseable; left, so it reaches the DLQ
)

// Handle applies one event and reports what to do with its message.
func Handle(ctx context.Context, m Transitioner, log *slog.Logger, body string) Outcome {
	e, err := Parse(body)
	if err != nil {
		log.Warn("status event rejected", "err", err)
		return Rejected
	}
	log = log.With("eventId", e.EventID, "type", e.Type, "videoId", e.VideoID)

	var (
		action lifecycle.Action
		ch     lifecycle.Changes
	)
	switch e.Type {
	case TypeProcessing:
		action = lifecycle.Processing
	case TypeReady:
		d := e.Ready.DurationSeconds
		action, ch = lifecycle.Ready, lifecycle.Changes{DurationSeconds: &d, Renditions: e.Ready.Renditions}
	case TypeFailed:
		action, ch = lifecycle.Fail, lifecycle.Changes{FailureReason: e.Failed.Reason}
	}

	err = m.Transition(ctx, e.VideoID, action, ch, nil)
	var conflict *video.ConflictError
	switch {
	case err == nil:
		log.Info("status event applied")
		return Applied
	case errors.Is(err, lifecycle.ErrNoop):
		log.Info("duplicate status event ignored")
		return Ignored
	case errors.As(err, &conflict):
		// The video already moved past this event (late or out-of-order delivery), or it
		// is in a final status. Nothing to do.
		log.Info("status event does not move the video forward; ignored", "current", conflict.Current)
		return Ignored
	case errors.Is(err, video.ErrNotFound):
		log.Warn("status event for an unknown video; deleted")
		return Ignored
	default:
		log.Error("status event failed; will be redelivered", "err", err)
		return Retry
	}
}

// Consumer long-polls the queue.
type Consumer struct {
	client   SQS
	queueURL string
	machine  Transitioner
	log      *slog.Logger

	// HandleTimeout bounds one message. It is shorter than the queue's visibility
	// timeout (60 s), so a slow message is not handled twice at once.
	HandleTimeout time.Duration
	// WaitTimeSeconds and MaxMessages configure the long poll.
	WaitTimeSeconds int32
	MaxMessages     int32
}

// NewConsumer returns a consumer for queueURL.
func NewConsumer(client SQS, queueURL string, m Transitioner, log *slog.Logger) *Consumer {
	if log == nil {
		log = slog.Default()
	}
	return &Consumer{
		client: client, queueURL: queueURL, machine: m, log: log.With("component", "status-consumer"),
		HandleTimeout: 20 * time.Second, WaitTimeSeconds: 20, MaxMessages: 10,
	}
}

// Run polls until ctx is cancelled (SIGTERM). Messages already received when that
// happens are finished before Run returns; a receive in progress is abandoned, and its
// messages become visible again after the visibility timeout.
func (c *Consumer) Run(ctx context.Context) {
	c.log.Info("status consumer started", "queue", c.queueURL)
	defer c.log.Info("status consumer stopped")
	backoff := time.Second
	for ctx.Err() == nil {
		out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queueURL),
			MaxNumberOfMessages: c.MaxMessages,
			WaitTimeSeconds:     c.WaitTimeSeconds,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Error("receive failed; retrying", "err", err, "in", backoff)
			sleep(ctx, backoff+time.Duration(rand.Int64N(int64(backoff/2+1))))
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		c.process(context.WithoutCancel(ctx), out.Messages)
	}
}

// process handles one batch concurrently. ctx is not cancelled by shutdown.
func (c *Consumer) process(ctx context.Context, msgs []types.Message) {
	var wg sync.WaitGroup
	for _, msg := range msgs {
		wg.Add(1)
		go func(msg types.Message) {
			defer wg.Done()
			c.ProcessMessage(ctx, msg)
		}(msg)
	}
	wg.Wait()
}

// ProcessMessage handles one message and deletes it when the outcome says so.
func (c *Consumer) ProcessMessage(ctx context.Context, msg types.Message) Outcome {
	hctx, cancel := context.WithTimeout(ctx, c.HandleTimeout)
	defer cancel()
	log := c.log.With("messageId", aws.ToString(msg.MessageId))
	outcome := Handle(hctx, c.machine, log, aws.ToString(msg.Body))
	if outcome == Applied || outcome == Ignored {
		_, err := c.client.DeleteMessage(hctx, &sqs.DeleteMessageInput{
			QueueUrl: aws.String(c.queueURL), ReceiptHandle: msg.ReceiptHandle,
		})
		if err != nil {
			// The event was applied; redelivery will be a no-op.
			log.Warn("delete message failed; it will be redelivered", "err", err)
		}
	}
	return outcome
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
