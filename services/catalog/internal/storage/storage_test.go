package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeS3 holds keys in memory, pages listings by pageSize, and records delete batches.
type fakeS3 struct {
	mu       sync.Mutex
	keys     map[string]bool
	pageSize int
	batches  []int
	listErr  error
	denyKey  string
}

func newFake(keys ...string) *fakeS3 {
	f := &fakeS3{keys: map[string]bool{}, pageSize: 1000}
	for _, k := range keys {
		f.keys[k] = true
	}
	return f
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var all []string
	for k := range f.keys {
		if strings.HasPrefix(k, aws.ToString(in.Prefix)) {
			all = append(all, k)
		}
	}
	sort.Strings(all)
	start := 0
	if in.ContinuationToken != nil {
		fmt.Sscanf(*in.ContinuationToken, "%d", &start)
	}
	end := min(start+f.pageSize, len(all))
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(end < len(all))}
	for _, k := range all[start:end] {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(k)})
	}
	if end < len(all) {
		out.NextContinuationToken = aws.String(fmt.Sprint(end))
	}
	return out, nil
}

func (f *fakeS3) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(in.Delete.Objects) > MaxDeleteBatch {
		return nil, errors.New("MalformedXML: more than 1000 keys")
	}
	f.batches = append(f.batches, len(in.Delete.Objects))
	out := &s3.DeleteObjectsOutput{}
	for _, o := range in.Delete.Objects {
		k := aws.ToString(o.Key)
		if k == f.denyKey {
			out.Errors = append(out.Errors, types.Error{Key: o.Key, Code: aws.String("AccessDenied")})
			continue
		}
		delete(f.keys, k) // missing keys succeed silently, like S3
	}
	return out, nil
}

const vid = "5eed1000-0000-4000-8000-000000000001"

func TestDeleteOutputs(t *testing.T) {
	f := newFake(
		"videos/"+vid+"/master.m3u8", "videos/"+vid+"/360p/index.m3u8", "videos/"+vid+"/360p/seg_000.ts",
		"thumbnails/"+vid+".jpg",
		"videos/"+vid+"0/master.m3u8",                      // another id sharing the prefix text
		"videos/5eed1000-0000-4000-8000-000000000002/x.ts", // another video
		"thumbnails/5eed1000-0000-4000-8000-000000000002.jpg",
	)
	n, err := NewCleaner(f, "b").DeleteOutputs(context.Background(), vid)
	if err != nil || n != 4 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(f.keys) != 3 {
		t.Errorf("remaining keys: %v", f.keys)
	}
	for k := range f.keys {
		if strings.HasPrefix(k, "videos/"+vid+"/") || k == "thumbnails/"+vid+".jpg" {
			t.Errorf("%s not deleted", k)
		}
	}
}

func TestPaginationAndBatching(t *testing.T) {
	var keys []string
	for i := 0; i < 2500; i++ {
		keys = append(keys, fmt.Sprintf("videos/%s/720p/seg_%04d.ts", vid, i))
	}
	f := newFake(keys...)
	f.pageSize = 300 // forces 9 list pages
	n, err := NewCleaner(f, "b").DeleteOutputs(context.Background(), vid)
	if err != nil || n != 2501 { // 2500 segments + the thumbnail key
		t.Fatalf("n=%d err=%v", n, err)
	}
	if fmt.Sprint(f.batches) != "[1000 1000 501]" {
		t.Errorf("batches = %v", f.batches)
	}
	if len(f.keys) != 0 {
		t.Errorf("%d keys left", len(f.keys))
	}
}

func TestMissingPrefixIsSuccess(t *testing.T) {
	f := newFake()
	n, err := NewCleaner(f, "b").DeleteOutputs(context.Background(), vid)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestErrors(t *testing.T) {
	f := newFake("videos/" + vid + "/master.m3u8")
	f.listErr = errors.New("AccessDenied")
	if _, err := NewCleaner(f, "b").DeleteOutputs(context.Background(), vid); err == nil {
		t.Error("list error ignored")
	}
	f = newFake("videos/" + vid + "/master.m3u8")
	f.denyKey = "videos/" + vid + "/master.m3u8"
	if _, err := NewCleaner(f, "b").DeleteOutputs(context.Background(), vid); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("per-key error: %v", err)
	}
}
