package lifecycle

import (
	"fmt"
	"os"
	"time"

	"github.com/standards-lab/go-core/config"
)

// defaultShutdownTimeout is the shutdown budget a zero [Config] finalizes
// to.
const defaultShutdownTimeout = 10 * time.Second

// Config is a [Coordinator]'s configuration, shaped by go-core's config
// conventions: a JSON block, merged by its owner, then finalized once.
type Config struct {
	// ShutdownTimeout bounds the whole shutdown, every layer together.
	ShutdownTimeout config.Duration `json:"shutdown_timeout"`
}

// Finalize applies the default ShutdownTimeout of 10s when it is unset, reads
// the override <PREFIX>_SHUTDOWN_TIMEOUT (composed by config.EnvName, so an
// empty prefix reads no variable), and validates. An unparsable override is
// an error naming the variable, and a ShutdownTimeout that is not positive
// is an error.
func (c *Config) Finalize(prefix string) error {
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = config.Duration(defaultShutdownTimeout)
	}

	name := config.EnvName(prefix, "shutdown_timeout")
	if err := c.ShutdownTimeout.Set(os.Getenv(name)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown_timeout must be positive, got %s", c.ShutdownTimeout)
	}
	return nil
}
