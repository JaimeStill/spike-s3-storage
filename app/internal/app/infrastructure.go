package app

import (
	"github.com/standards-lab/go-database"
	"github.com/standards-lab/go-database/postgres"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/sqlate"
	sqlpostgres "github.com/standards-lab/sqlate/postgres"

	"github.com/JaimeStill/spike-s3-storage/s3"

	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
)

// envPrefix is the prefix of every environment variable blobfs reads its
// configuration from: the libraries compose the rest, such as
// BLOBFS_DATABASE_HOST, BLOBFS_STORAGE_ENDPOINT, or BLOBFS_SHUTDOWN_TIMEOUT.
const envPrefix = "BLOBFS"

// defineInfrastructure defines the infrastructure nodes on g into n: each
// configuration, and the database, its sqlate wrapping, and the object
// store built from them. It constructs nothing. Every value is read or
// constructed in its node's constructor, which runs only when a Build
// reaches the node, so configuration is read only for a built System,
// never in [New] and never for a run that builds nothing.
//
// *database.DB and *storage.Store each implement lifecycle.Subsystem, a
// Start (a ping, a probe) and a Shutdown, so the lifecycle starts and shuts
// them down through their own methods.
func defineInfrastructure(g *graph.Graph, n *Nodes) {
	n.DatabaseConfig = g.Define("database config", finalized[database.Config])
	n.StorageConfig = g.Define("storage config", finalized[storage.Config])
	n.LifecycleConfig = g.Define("lifecycle config", finalized[lifecycle.Config])
	n.Database = g.Define("database", newDatabase(n))
	n.SQL = g.Define("sql", newSQL(n))
	n.Store = g.Define("store", newStore(n))
}

// finalizer is a configuration finalized from the environment under a
// prefix, as go-core's config convention shapes go-database's, go-storage's,
// and lifecycle's Config.
type finalizer[T any] interface {
	*T
	Finalize(prefix string) error
}

// finalized is a configuration node's constructor: it reads the zero
// configuration's defaults and its environment overrides under envPrefix
// alone, and validates them.
func finalized[T any, P finalizer[T]](*graph.Scope) (T, error) {
	var cfg T
	err := P(&cfg).Finalize(envPrefix)
	return cfg, err
}

// newDatabase constructs the Postgres pool from the database configuration,
// BLOBFS_DATABASE_HOST, _PORT, _NAME, _USER, _PASSWORD, and the pool and
// timeout settings go-database names. It does no I/O: the pool first
// connects in Start, the ping bounded by the configuration's conn_timeout.
func newDatabase(n *Nodes) func(*graph.Scope) (*database.DB, error) {
	return func(s *graph.Scope) (*database.DB, error) {
		return postgres.New(s.Use(n.DatabaseConfig))
	}
}

// newSQL constructs the database's pool wrapped in sqlate's Postgres
// dialect, the one *sqlate.DB the migrator and the files Service both use.
// It does no I/O, and records no start of its own: the pool is the
// database's, which the lifecycle starts and shuts down through the
// database node.
func newSQL(n *Nodes) func(*graph.Scope) (*sqlate.DB, error) {
	return func(s *graph.Scope) (*sqlate.DB, error) {
		return sqlate.Wrap(s.Use(n.Database).Conn(), sqlpostgres.Dialect{}), nil
	}
}

// newStore constructs the object store over this repository's S3 provider
// from the storage configuration, BLOBFS_STORAGE_ENDPOINT, _CONTAINER (the
// bucket), _ACCOUNT (the access key), _KEY (the secret key), the limits and
// timeouts go-storage names, and the s3 options under
// BLOBFS_STORAGE_OPTIONS_ (_REGION, _MAX_RETRIES, _PART_SIZE). It does no
// I/O: Start creates the bucket when it is missing and probes the service,
// both bounded by the configuration's request_timeout.
func newStore(n *Nodes) func(*graph.Scope) (*storage.Store, error) {
	return func(s *graph.Scope) (*storage.Store, error) {
		cfg := s.Use(n.StorageConfig)
		client, err := s3.New(cfg)
		if err != nil {
			return nil, err
		}
		return storage.New(client, cfg), nil
	}
}
