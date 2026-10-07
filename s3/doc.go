// Package s3 is a go-storage provider over the S3 API. Its [Client]
// implements storage.Client over one bucket, built on aws-sdk-go-v2's
// service/s3 and authenticated with a static access key, and [New]
// constructs it. It is validated against SeaweedFS's S3 gateway. The
// sections below state how the Client maps storage's contract onto S3.
//
// # Construction
//
// [New] builds a [Client] from a finalized storage.Config without I/O.
// Container, Account, and Key are required: Container is the bucket, Account
// the access key ID, and Key the secret access key. Container must be a valid
// general purpose bucket name: 3 to 63 lowercase letters, digits, dots, and
// hyphens, starting and ending with a letter or digit, with no two dots
// together, not shaped like an IPv4 address, and free of the prefixes and
// suffixes S3 reserves.
//
//	client, err := s3.New(cfg)
//	if err != nil {
//		return err
//	}
//	store := storage.New(client, cfg)
//
// # Endpoints
//
// An empty Endpoint means AWS's own endpoint for the region, addressed
// virtual-hosted style as the SDK resolves it. A set Endpoint must be an
// absolute http or https URL. It becomes the SDK's base endpoint with
// path-style addressing, so the bucket is the first path segment, which is
// what an S3-compatible gateway such as SeaweedFS's expects.
//
// # Options
//
// Config.Options carries the provider's own settings, each overridable
// through storage.Env. Any key not listed here is ignored.
//
//   - region: the region the requests are signed for and, against AWS, the
//     region the bucket is created in. Unset means us-east-1. An S3-compatible
//     gateway usually accepts any region in the signature.
//   - max_retries: how many times the SDK retries a request that failed with
//     a transport error or a retryable answer (a 5xx status, a throttling
//     code), as a non-negative integer. 0 means one try. Unset keeps the SDK
//     default: three attempts, with jittered exponential backoff.
//
// A malformed value is a construction error. The SDK's default checksum
// behavior is kept: a request carries a checksum when the operation
// supports one.
//
// # Containers
//
// [Client.EnsureContainer] sends CreateBucket, with the region as its
// location constraint everywhere but us-east-1, which takes none. It treats
// BucketAlreadyOwnedByYou as success. It treats BucketAlreadyExists as
// success only when a HeadBucket that follows it succeeds, because some
// gateways answer an owner's re-create with that code, while AWS uses it for
// a bucket another account owns. It never deletes or reconfigures a bucket.
// [Client.Probe] sends HeadBucket, which proves the endpoint, the
// credential, and the bucket together.
//
// # Keys
//
// [Client.Capabilities] declares the S3 key rules: a key is non-empty valid
// UTF-8 of at most 1,024 bytes. MaxKeyLength counts bytes, not runes.
//
// # Errors
//
// Every method classifies the SDK's error in the dual form, so errors.As
// still reaches the SDK's smithy.APIError and its HTTP response error.
// NoSuchBucket matches storage.ErrContainerNotFound, and so does a 404 on a
// bucket request, whose HEAD answer carries no code. A 5xx answer, the
// SlowDown, ServiceUnavailable, and InternalError codes, and a failure with
// no response, an expired deadline included, match storage.ErrUnavailable.
// The caller's cancellation and every other answer, an authentication
// failure included, pass through unclassified.
//
// # Acceptance against SeaweedFS
//
// The unit tests run against a scripted HTTP server. The acceptance tests
// run storage.Store.Start, Probe, and EnsureContainer against a real gateway
// when S3_TEST_ENDPOINT names its URL, each in a bucket of its own, with the
// access key admin and the secret secret. The repository's mise tasks start
// SeaweedFS with those credentials and run them:
//
//	mise run seaweedfs:start
//	mise run acceptance
//	mise run seaweedfs:stop
package s3
