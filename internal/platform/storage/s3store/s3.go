// Package s3store implements storage.Storage on any S3-compatible service
// (AWS S3, Cloudflare R2, MinIO, ...).
package s3store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"pms/internal/platform/storage"
)

type Config struct {
	Endpoint       string // empty = AWS S3
	PublicEndpoint string // host embedded in presigned URLs; defaults to Endpoint
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	PathStyle      bool
	AutoCreate     bool
}

type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func newClient(c Config, endpoint string) *s3.Client {
	o := s3.Options{
		Region:       c.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
		UsePathStyle: c.PathStyle,
	}
	if endpoint != "" {
		o.BaseEndpoint = aws.String(endpoint)
	}
	return s3.New(o)
}

func New(ctx context.Context, c Config) (*Store, error) {
	if c.PublicEndpoint == "" {
		c.PublicEndpoint = c.Endpoint
	}
	s := &Store{
		client:  newClient(c, c.Endpoint),
		presign: s3.NewPresignClient(newClient(c, c.PublicEndpoint)),
		bucket:  c.Bucket,
	}
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket}); err != nil {
		if !c.AutoCreate {
			return nil, fmt.Errorf("bucket %q is not accessible (create it, or set STORAGE_AUTO_CREATE_BUCKET=true): %w", c.Bucket, err)
		}
		in := &s3.CreateBucketInput{Bucket: &s.bucket}
		if c.Endpoint == "" && c.Region != "" && c.Region != "us-east-1" {
			in.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(c.Region)}
		}
		if _, cerr := s.client.CreateBucket(ctx, in); cerr != nil {
			return nil, fmt.Errorf("create bucket %q: %w", c.Bucket, cerr)
		}
	}
	return s, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	body, ok := r.(io.ReadSeeker)
	if !ok { // SigV4 over plain HTTP needs a seekable body to hash it
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		body, size = bytes.NewReader(b), int64(len(b))
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &key, Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(contentType),
	})
	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}

func (s *Store) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *Store) List(ctx context.Context, prefix string, fn func(storage.Object) error) error {
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, o := range page.Contents {
			if err := fn(storage.Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size), LastModified: aws.ToTime(o.LastModified)}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	return err
}
