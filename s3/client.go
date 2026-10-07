package s3

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/standards-lab/go-storage"
)

// The Options keys this package reads. See the package documentation for
// their values.
const (
	optionRegion     = "region"
	optionMaxRetries = "max_retries"
	optionPartSize   = "part_size"
)

// The part_size option's default and bounds. S3 refuses a part, the last
// one excepted, below 5 MiB, and any part above 5 GiB.
const (
	defaultPartSize = 8 << 20
	minPartSize     = 5 << 20
	maxPartSize     = 5 << 30
)

// abortTimeout bounds the AbortMultipartUpload that follows a failed
// multipart upload. It runs on a context detached from the caller's, so a
// cancelled Put still aborts what it started.
const abortTimeout = 30 * time.Second

// defaultRegion is the region option's default, and the one region whose
// CreateBucket takes no location constraint.
const defaultRegion = "us-east-1"

var _ storage.Client = (*Client)(nil)

// Client is the S3 provider: a storage.Client over one bucket, authenticated
// with a static access key. A Client is safe for concurrent use.
type Client struct {
	s3       *awss3.Client
	uploader *transfermanager.Client
	bucket   string
	region   string
	partSize int64
}

// New constructs a Client from a finalized config without I/O, as the
// package documentation describes. A missing field, an invalid bucket name,
// a malformed endpoint, or a malformed option is an error; an unfinalized
// config panics.
func New(cfg storage.Config) (*Client, error) {
	if !cfg.Finalized() {
		panic("s3: Config not finalized: call Finalize before New")
	}
	if cfg.Container == "" {
		return nil, errors.New("s3: storage container (bucket) required")
	}
	if err := validateBucket(cfg.Container); err != nil {
		return nil, err
	}
	if cfg.Account == "" {
		return nil, errors.New("s3: storage account (access key ID) required")
	}
	if cfg.Key == "" {
		return nil, errors.New("s3: storage key (secret access key) required")
	}

	region, err := regionOption(cfg.Options)
	if err != nil {
		return nil, err
	}
	attempts, err := maxAttempts(cfg.Options)
	if err != nil {
		return nil, err
	}
	partSize, err := partSizeOption(cfg.Options)
	if err != nil {
		return nil, err
	}

	// awss3.New, unlike config.LoadDefaultConfig, reads no environment
	// variable or shared file, so the Config is the client's only input.
	opts := awss3.Options{
		Region:           region,
		Credentials:      credentials.NewStaticCredentialsProvider(cfg.Account, cfg.Key, ""),
		RetryMaxAttempts: attempts,
	}
	if cfg.Endpoint != "" {
		if err := validateEndpoint(cfg.Endpoint); err != nil {
			return nil, err
		}
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
		opts.UsePathStyle = true
	}
	client := awss3.New(opts)
	// The threshold equal to the part size makes transfermanager take any
	// body of at least one part as a multipart upload; Put sends a smaller
	// one as a PutObject itself, so it only hands over a body longer than
	// a part. FailTimeout gives the abort after a failure a fresh context,
	// so the caller's cancellation does not cancel the abort too.
	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = partSize
		o.MultipartUploadThreshold = partSize
		o.FailTimeout = abortTimeout
	})
	return &Client{s3: client, uploader: uploader, bucket: cfg.Container, region: region, partSize: partSize}, nil
}

// regionOption reads the region option, applying the default for an unset
// key.
func regionOption(options map[string]string) (string, error) {
	v, ok := options[optionRegion]
	if !ok {
		return defaultRegion, nil
	}
	if v == "" {
		return "", fmt.Errorf("s3: option %s: must not be empty", optionRegion)
	}
	return v, nil
}

// maxAttempts reads the max_retries option as the SDK's RetryMaxAttempts,
// which counts the first try. An unset key returns 0, which the SDK reads as
// "apply the default".
func maxAttempts(options map[string]string) (int, error) {
	v, ok := options[optionMaxRetries]
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("s3: option %s: %q is not a non-negative integer", optionMaxRetries, v)
	}
	return n + 1, nil
}

// partSizeOption reads the part_size option as a whole number of bytes
// within the bounds S3 sets on a part, applying the default for an unset
// key.
func partSizeOption(options map[string]string) (int64, error) {
	v, ok := options[optionPartSize]
	if !ok {
		return defaultPartSize, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < minPartSize || n > maxPartSize {
		return 0, fmt.Errorf("s3: option %s: %q is not a byte count from %d (5 MiB) to %d (5 GiB)", optionPartSize, v, int64(minPartSize), int64(maxPartSize))
	}
	return n, nil
}

// validateEndpoint reports whether endpoint is an absolute http or https
// URL, so a typo fails at construction rather than on the first request.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("s3: endpoint %q: %w", endpoint, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("s3: endpoint %q is not an absolute http or https URL", endpoint)
	}
	return nil
}

// EnsureContainer creates the bucket, treating BucketAlreadyOwnedByYou as
// success, and BucketAlreadyExists as success when the bucket then answers
// a probe, as the package documentation describes.
func (c *Client) EnsureContainer(ctx context.Context) error {
	in := &awss3.CreateBucketInput{Bucket: aws.String(c.bucket)}
	if c.region != defaultRegion {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(c.region),
		}
	}
	_, err := c.s3.CreateBucket(ctx, in)
	switch errorCode(err) {
	case "BucketAlreadyOwnedByYou":
		return nil
	case "BucketAlreadyExists":
		if probeErr := c.Probe(ctx); probeErr != nil {
			return fmt.Errorf("%w (probe after it: %w)", classify(err), probeErr)
		}
		return nil
	}
	return classify(err)
}

// Probe sends HeadBucket, which proves the endpoint, the credential, and the
// bucket together. HEAD answers carry no body, so a missing bucket arrives
// as a bare 404 the SDK names NotFound; on a bucket request that can only
// mean the bucket.
func (c *Client) Probe(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if errorCode(err) == "NotFound" {
		return fmt.Errorf("%w: %w", storage.ErrContainerNotFound, err)
	}
	return classify(err)
}

// Capabilities returns the key rules the package documentation lists.
func (c *Client) Capabilities() storage.Capabilities {
	return storage.Capabilities{
		MaxKeyLength: maxKeyLength,
		ValidateKey:  validateKey,
	}
}
