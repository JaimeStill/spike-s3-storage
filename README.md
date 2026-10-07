# spike-s3-storage

A one-off spike: an S3 provider for [go-storage](https://github.com/standards-lab/go-storage)
v0.4.0, built on aws-sdk-go-v2 and validated against SeaweedFS's S3 gateway. It depends on
published versions only and contains no `replace` directive.

## The question

Does go-storage's `Client` interface hold over S3, using an aws-sdk-go-v2 provider validated
against SeaweedFS 4.48's S3 gateway — and what does SeaweedFS's S3 change for the adapter?

## Evidence

1. `storagetest.Run` passes against SeaweedFS 4.48's S3 gateway — acceptance test. *Pending.*
2. `storagetest.RunMissingContainer` passes — acceptance test. *Pending.*
3. `Store.Start` creates a missing bucket, probes, and reports ready; an unreachable endpoint
   yields `ErrUnavailable` — acceptance test. *Pending.*
4. `EnsureContainer` on an existing bucket succeeds and changes nothing — acceptance test.
   *Pending.*
5. A `Put` above the part size is invisible to `Stat`/`List` until complete; a body failing
   partway leaves no object and no open multipart upload — acceptance test. *Pending.*
6. `Stat` on a missing bucket yields `ErrContainerNotFound`, never `ErrNotFound` — acceptance
   test. *Pending.*
7. Error classification, ETag quoting, and key validation hold against a scripted S3 stand-in
   — unit tests only. *Pending.*
8. The SeaweedFS-vs-AWS differences the provider absorbs, or that only AWS docs support —
   finding, no test. *Pending.*

## The answer

Pending.

## Running it

Pending.
