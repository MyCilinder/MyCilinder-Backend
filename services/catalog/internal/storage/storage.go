// Package storage removes the HLS outputs of deleted videos from the hls-output bucket
// (Data Design §2.3). A lifecycle rule cannot do this, because it cannot see the status.
//
// For one video it deletes every object under videos/{id}/ and the thumbnail
// thumbnails/{id}.jpg. The raw upload is in another bucket and is never touched (BR-05).
package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// MaxDeleteBatch is the most keys one DeleteObjects call accepts.
const MaxDeleteBatch = 1000

// S3 is the subset of the S3 client the cleaner uses. *s3.Client implements it.
type S3 interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, opts ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// Cleaner deletes outputs from one bucket.
type Cleaner struct {
	client S3
	bucket string
}

// NewCleaner returns a Cleaner for bucket.
func NewCleaner(client S3, bucket string) *Cleaner { return &Cleaner{client: client, bucket: bucket} }

// DeleteOutputs removes the video's HLS prefix and thumbnail and returns how many keys it
// asked S3 to delete. Missing objects count as success, so a repeated call is harmless.
func (c *Cleaner) DeleteOutputs(ctx context.Context, videoID string) (int, error) {
	keys := []string{"thumbnails/" + videoID + ".jpg"}
	prefix := "videos/" + videoID + "/"
	p := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket), Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return 0, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}

	for start := 0; start < len(keys); start += MaxDeleteBatch {
		batch := keys[start:min(start+MaxDeleteBatch, len(keys))]
		objs := make([]types.ObjectIdentifier, len(batch))
		for i, k := range batch {
			objs[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}
		out, err := c.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucket),
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return 0, fmt.Errorf("delete outputs of %s: %w", videoID, err)
		}
		// Deleting a key that does not exist is not an error in S3, so any reported
		// error is real (for example AccessDenied).
		if len(out.Errors) > 0 {
			var errs []error
			for _, e := range out.Errors {
				errs = append(errs, fmt.Errorf("%s: %s %s", aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message)))
			}
			return 0, fmt.Errorf("delete outputs of %s: %w", videoID, errors.Join(errs...))
		}
	}
	return len(keys), nil
}
