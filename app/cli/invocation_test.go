package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/config"
	"github.com/standards-lab/go-core/process"
)

// getTree is a test tree for Invocation.Get: prog { data { both, only },
// store, bare }. data declares db, both adds store, store declares store
// alone (which Uses db, so its Build reaches db undeclared), and only and
// bare add nothing. Each node's value is its name. Every leaf runs read,
// which the test sets.
type getTree struct {
	root      *cli.Command
	g         *graph.Graph
	cfg       *graph.Node[lifecycle.Config]
	db, store *graph.Node[string]
	read      func(inv *cli.Invocation) error
}

func newGetTree() *getTree {
	gt := &getTree{g: graph.New()}
	gt.cfg = gt.g.Define("lifecycle", func(*graph.Scope) (lifecycle.Config, error) {
		return lifecycle.Config{ShutdownTimeout: config.Duration(5 * time.Second)}, nil
	})
	gt.db = gt.g.Define("db", func(*graph.Scope) (string, error) { return "db value", nil })
	gt.store = gt.g.Define("store", func(s *graph.Scope) (string, error) {
		return "store over " + s.Use(gt.db), nil
	})
	leaf := func(name string) *cli.Command {
		return &cli.Command{Name: name, Run: func(_ context.Context, inv *cli.Invocation) error {
			return gt.read(inv)
		}}
	}
	gt.root = (&cli.Command{Name: "prog"}).Add(
		(&cli.Command{Name: "data"}).Use(gt.db).Add(leaf("both").Use(gt.store), leaf("only")),
		leaf("store").Use(gt.store),
		leaf("bare"),
	)
	return gt
}

func (gt *getTree) run(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), gt.root, args, cli.Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, cli.WithGraph(gt.g, gt.cfg))
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// panicOf runs fn and returns what it panicked with, or nil.
func panicOf(fn func()) (r any) {
	defer func() { r = recover() }()
	fn()
	return nil
}

func TestGet_ReturnsEachDeclaredNodesValue(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want map[string]string // node name to the value Get returns
	}{
		{"declared on the leaf and inherited", []string{"data", "both"}, map[string]string{"db": "db value", "store": "store over db value"}},
		{"inherited from the parent alone", []string{"data", "only"}, map[string]string{"db": "db value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gt := newGetTree()
			got := map[string]string{}
			gt.read = func(inv *cli.Invocation) error {
				got["db"] = inv.Get(gt.db)
				if len(tt.want) > 1 {
					got["store"] = inv.Get(gt.store)
				}
				return nil
			}

			r := gt.run(tt.args...)

			if r.code != process.ExitOK {
				t.Fatalf("code = %d, want %d; stderr %q", r.code, process.ExitOK, r.stderr)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("Get returned %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGet_PanicsNamingThePathAndTheNode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		get  func(gt *getTree, inv *cli.Invocation)
		want string
	}{
		{
			"a leaf that declares nothing",
			[]string{"bare"},
			func(gt *getTree, inv *cli.Invocation) { inv.Get(gt.db) },
			`cli: prog bare: Get of node "db", which the command's path does not declare with Use`,
		},
		{
			"a node another path declares",
			[]string{"data", "only"},
			func(gt *getTree, inv *cli.Invocation) { inv.Get(gt.store) },
			`cli: prog data only: Get of node "store", which the command's path does not declare with Use`,
		},
		{
			// store Uses db, so db is in the System, but the path does not
			// declare it.
			"a node the Build reached undeclared",
			[]string{"store"},
			func(gt *getTree, inv *cli.Invocation) { inv.Get(gt.db) },
			`cli: prog store: Get of node "db", which the command's path does not declare with Use`,
		},
		{
			"a nil node",
			[]string{"data", "only"},
			func(_ *getTree, inv *cli.Invocation) { inv.Get((*graph.Node[string])(nil)) },
			"cli: prog data only: Get of a nil node",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gt := newGetTree()
			gt.read = func(inv *cli.Invocation) error {
				tt.get(gt, inv)
				return nil
			}

			got := panicOf(func() { gt.run(tt.args...) })

			if got != tt.want {
				t.Errorf("panic = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestGet_PanicsBeforeTheBuild(t *testing.T) {
	tests := []struct {
		name string
		args []string
		set  func(gt *getTree)
	}{
		{"in the root's PreRun", []string{"data", "only"}, func(gt *getTree) {
			gt.root.PreRun = func(_ context.Context, inv *cli.Invocation) error {
				inv.Get(gt.db)
				return nil
			}
		}},
		{"in Validate", []string{"check"}, func(gt *getTree) {
			gt.root.Add((&cli.Command{
				Name:     "check",
				Validate: func(inv *cli.Invocation) error { inv.Get(gt.db); return nil },
				Run:      func(context.Context, *cli.Invocation) error { return nil },
			}).Use(gt.db))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gt := newGetTree()
			gt.read = func(*cli.Invocation) error { return nil }
			tt.set(gt)

			got := panicOf(func() { gt.run(tt.args...) })

			want := fmt.Sprintf(`cli: prog %s: Get of node "db" before the Build; only Run can read a node`, strings.Join(tt.args, " "))
			if got != want {
				t.Errorf("panic = %v, want %q", got, want)
			}
		})
	}
}
