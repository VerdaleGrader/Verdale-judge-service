package testfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNotFound means the object does not exist. Retrying will not help.
var ErrNotFound = errors.New("test file not found in storage")

// ObjectStore is the read-only view of the test bucket Judge needs. Judge
// only ever holds read credentials, so there is no write method.
type ObjectStore interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// LocalStore reads objects from a directory. For development only.
type LocalStore struct {
	Dir string
}

func (s LocalStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if strings.Contains(key, "..") {
		return nil, fmt.Errorf("invalid key %q", key)
	}
	file, err := os.Open(filepath.Join(s.Dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return file, err
}

// S3Store reads objects over the S3 API. Production points it at the
// private Cloudflare R2 test bucket with a read-only token.
type S3Store struct {
	client *s3.Client
	bucket string
}

type S3Options struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

func NewS3Store(options S3Options) *S3Store {
	client := s3.New(s3.Options{
		Region:       options.Region,
		BaseEndpoint: aws.String(options.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(options.AccessKeyID, options.SecretAccessKey, ""),
		UsePathStyle: true,
	})
	return &S3Store{client: client, bucket: options.Bucket}
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}
