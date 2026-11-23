// Command send-event publishes a video status event, for local development only. It
// stands in for the Transcoder (Interface Design §2.1).
//
//	go run ./services/catalog/cmd/send-event --type video.processing --video <id>
//	go run ./services/catalog/cmd/send-event --type video.ready --video <id> --duration 30 --renditions 360p,720p
//	go run ./services/catalog/cmd/send-event --type video.failed --video <id> --reason "unsupported codec"
//
// By default it publishes to the SNS topic in VIDEO_EVENTS_TOPIC_ARN (or --topic), the
// same path the Transcoder uses. --sqs sends straight to VIDEO_STATUS_QUEUE_URL (or
// --queue) instead. --raw sends the given body unchanged, to test rejected messages.
//
// Credentials come from the default AWS chain (AWS_PROFILE locally). It needs
// sns:Publish on the topic, or sqs:SendMessage on the queue with --sqs.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "send-event:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		typ        = flag.String("type", "", "video.processing, video.ready, or video.failed")
		videoID    = flag.String("video", "", "video id")
		duration   = flag.Int("duration", 0, "durationSeconds (video.ready)")
		renditions = flag.String("renditions", "360p,720p", "comma-separated renditions (video.ready)")
		reason     = flag.String("reason", "", "failure reason (video.failed)")
		raw        = flag.String("raw", "", "send this body unchanged instead of building an envelope")
		useSQS     = flag.Bool("sqs", false, "send straight to the queue instead of the topic")
		topic      = flag.String("topic", os.Getenv("VIDEO_EVENTS_TOPIC_ARN"), "SNS topic ARN")
		queue      = flag.String("queue", os.Getenv("VIDEO_STATUS_QUEUE_URL"), "SQS queue URL (with --sqs)")
	)
	flag.Parse()

	body := *raw
	if body == "" {
		if *typ == "" || *videoID == "" {
			flag.Usage()
			return errors.New("--type and --video are required")
		}
		data := map[string]any{}
		switch *typ {
		case "video.processing":
		case "video.ready":
			data["durationSeconds"] = *duration
			data["renditions"] = strings.Split(*renditions, ",")
		case "video.failed":
			data["reason"] = *reason
		default:
			return fmt.Errorf("unknown --type %q", *typ)
		}
		b, _ := json.Marshal(map[string]any{
			"eventId":    newID(),
			"type":       *typ,
			"videoId":    *videoID,
			"occurredAt": time.Now().UTC().Format(time.RFC3339),
			"data":       data,
		})
		body = string(b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}

	if *useSQS {
		if *queue == "" {
			return errors.New("set VIDEO_STATUS_QUEUE_URL or --queue")
		}
		out, err := sqs.NewFromConfig(cfg).SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: queue, MessageBody: aws.String(body)})
		if err != nil {
			return err
		}
		fmt.Printf("sent to queue: message %s\n%s\n", aws.ToString(out.MessageId), body)
		return nil
	}
	if *topic == "" {
		return errors.New("set VIDEO_EVENTS_TOPIC_ARN or --topic")
	}
	out, err := sns.NewFromConfig(cfg).Publish(ctx, &sns.PublishInput{TopicArn: topic, Message: aws.String(body)})
	if err != nil {
		return err
	}
	fmt.Printf("published to topic: message %s\n%s\n", aws.ToString(out.MessageId), body)
	return nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
