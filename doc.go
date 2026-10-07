// Package spike is the root of a one-off spike: it asks whether go-storage's
// Client interface holds over S3, through an aws-sdk-go-v2 provider validated
// against SeaweedFS's S3 gateway. The provider lives in the s3 sub-module;
// this module holds nothing else.
package spike
