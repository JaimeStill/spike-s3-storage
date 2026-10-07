package s3

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"unicode/utf8"
)

// maxKeyLength is the longest key S3 accepts, in bytes of UTF-8.
const maxKeyLength = 1024

// The bucket-name length limits the package documentation lists.
const (
	minBucketLength = 3
	maxBucketLength = 63
)

// The bucket-name prefixes and suffixes S3 reserves for its own features.
var (
	reservedBucketPrefixes = []string{"xn--", "sthree-", "amzn-s3-demo-"}
	reservedBucketSuffixes = []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"}
)

// validateKey reports whether S3 accepts key as an object key, with an
// error that says which rule it breaks.
func validateKey(key string) error {
	if key == "" {
		return errors.New("s3: empty key")
	}
	if n := len(key); n > maxKeyLength {
		return fmt.Errorf("s3: key is %d bytes, the limit is %d", n, maxKeyLength)
	}
	if !utf8.ValidString(key) {
		return errors.New("s3: key is not valid UTF-8")
	}
	return nil
}

// validateBucket reports whether S3 accepts name as a general purpose
// bucket name, with an error that says which rule it breaks.
func validateBucket(name string) error {
	if n := len(name); n < minBucketLength || n > maxBucketLength {
		return fmt.Errorf("s3: bucket name %q is %d characters, want %d to %d", name, n, minBucketLength, maxBucketLength)
	}
	for i := 0; i < len(name); i++ {
		switch b := name[i]; {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		case b == '-' || b == '.':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("s3: bucket name %q must start and end with a letter or digit", name)
			}
		default:
			return fmt.Errorf("s3: bucket name %q has %q; want lowercase letters, digits, dots, and hyphens", name, b)
		}
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("s3: bucket name %q has two dots together", name)
	}
	if addr, err := netip.ParseAddr(name); err == nil && addr.Is4() {
		return fmt.Errorf("s3: bucket name %q is shaped like an IP address", name)
	}
	for _, p := range reservedBucketPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("s3: bucket name %q has the reserved prefix %q", name, p)
		}
	}
	for _, s := range reservedBucketSuffixes {
		if strings.HasSuffix(name, s) {
			return fmt.Errorf("s3: bucket name %q has the reserved suffix %q", name, s)
		}
	}
	return nil
}
