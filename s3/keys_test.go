package s3_test

import (
	"strings"
	"testing"
)

func TestValidateKey_Accepts(t *testing.T) {
	validate := newClient(t, testConfig(t, "http://127.0.0.1:8333", nil)).Capabilities().ValidateKey

	for name, key := range map[string]string{
		"plain":            "a",
		"nested":           "dir/sub/file.txt",
		"1024 bytes":       strings.Repeat("k", 1024),
		"multibyte":        "日本語/ファイル",
		"1024 bytes utf-8": strings.Repeat("é", 512),
		"trailing slash":   "dir/",
		"trailing dot":     "file.",
		"control char":     "a\x01b",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(key); err != nil {
				t.Errorf("ValidateKey(%q) = %v, want nil", key, err)
			}
		})
	}
}

func TestValidateKey_Rejects(t *testing.T) {
	validate := newClient(t, testConfig(t, "http://127.0.0.1:8333", nil)).Capabilities().ValidateKey

	for name, tc := range map[string]struct{ key, want string }{
		"empty":                {"", "empty key"},
		"1025 bytes":           {strings.Repeat("k", 1025), "1025 bytes"},
		"1026 bytes of utf-8":  {strings.Repeat("é", 513), "1026 bytes"},
		"invalid utf-8":        {"a\xffb", "UTF-8"},
		"truncated multi-byte": {"\xe6\x97", "UTF-8"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validate(tc.key)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateKey = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
