package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/standards-lab/go-storage"
)

// classify maps an SDK error into the storage sentinels as the package
// documentation lists; nil and an already classified error pass through.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrContainerNotFound) ||
		errors.Is(err, storage.ErrUnavailable) {
		return err
	}

	// The SDK wraps a send failure in a ResponseError too, one with no
	// response and status 0, so an answer is a ResponseError with a status.
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() == 0 {
		// No answer arrived: the endpoint was unreachable, the deadline
		// passed, or the caller cancelled. Only the last passes through.
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("%w: %w", storage.ErrUnavailable, err)
	}

	switch code := errorCode(err); {
	case code == "NoSuchBucket":
		return fmt.Errorf("%w: %w", storage.ErrContainerNotFound, err)
	case respErr.HTTPStatusCode() >= http.StatusInternalServerError,
		code == "SlowDown", code == "ServiceUnavailable", code == "InternalError":
		return fmt.Errorf("%w: %w", storage.ErrUnavailable, err)
	}
	return err
}

// errorCode returns the S3 error code err carries, or "" when it carries
// none.
func errorCode(err error) string {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorCode()
	}
	return ""
}
