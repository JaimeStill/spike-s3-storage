package scenario

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/standards-lab/blobfs"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// filesArea is the files tour's working area, a top-level directory the
// tour creates, works under, and removes with rm --recursive, and clears
// first when an earlier run left it behind.
const filesArea = "/scenario-files"

// The files tour's content, held in memory: hello is put with no size, as
// put - streams standard input, and notes with its size, as put of a local
// file states it.
const (
	hello = "hello from the files tour\n"
	notes = "put, cat, cp, and rm run on Postgres and the object store.\n"
)

// filesTour is the files tour over svc, the files node, and st, the
// storage node: put, from memory as put - reads standard input and with a
// stated size, cat, cp, rm, and rm --recursive, in a working area of its
// own. It declares both nodes, since its steps read both, so a run builds
// Postgres and the object store, and a store that cannot be reached fails
// it at start, naming the store's node. Each step closes over the nodes it
// reads and reads their values with inv.Get.
func filesTour(svc *graph.Node[*files.Service], st *graph.Node[*files.Storage]) Scenario {
	return Scenario{
		Name:    "files",
		Summary: "Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive",
		Nodes:   []graph.Ref{svc, st},
		Steps: []Step{
			{Intent: "Clear " + filesArea + " if an earlier run left it behind", Action: clearFiles(svc, st)},
			{Intent: "Create " + filesArea + " and " + filesArea + "/docs: mkdir", Action: mkdirFilesArea(svc)},
			{Intent: "Put hello.txt from memory with no size, as put - streams stdin", Action: putStream(st)},
			{Intent: "Put notes.txt with its size stated, as put of a local file", Action: putSized(st)},
			{Intent: "Read hello.txt back: cat", Action: catHello(st)},
			{Intent: "Copy hello.txt into " + filesArea + " and read the copy: cp, cat", Action: copyHello(st)},
			{Intent: "List the working area and docs: ls", Action: listFilesArea(svc)},
			{Intent: "Remove the copy: rm", Action: removeCopy(st)},
			{Intent: "Remove the working area and everything under it: rm --recursive", Action: removeFilesTree(st)},
		},
	}
}

func clearFiles(svc *graph.Node[*files.Service], st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		_, err := inv.Get(svc).Resolve(ctx, files.Ref{Path: filesArea})
		if errors.Is(err, blobfs.ErrNotFound) {
			r.Note("Nothing to clear: %s does not exist, so this run starts clean.", filesArea)
			return nil
		}
		if err != nil {
			return err
		}
		r.Note("An earlier run stopped before its last step and left %s behind; rm --recursive removes it, objects and rows, so this run starts clean.", filesArea)
		return removeFilesArea(ctx, inv.Get(st), r)
	}
}

func mkdirFilesArea(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		s := inv.Get(svc)
		for _, path := range []string{filesArea, filesArea + "/docs"} {
			dir, err := s.Mkdir(ctx, files.Ref{Path: path}, "")
			if err != nil {
				return err
			}
			if err := r.showf("mkdir: %s (id %s)\n", path, dir.ID); err != nil {
				return err
			}
		}
		return nil
	}
}

// put writes content to path and shows the result as put prints it.
func put(ctx context.Context, st *files.Storage, r *Reporter, path string, c files.Content) error {
	res, err := st.Put(ctx, files.Ref{Path: path}, c)
	if err != nil {
		return err
	}
	f := res.File
	return r.showf("put: %s (id %s, %s, etag %s)\n", path, f.ID, output.Count(sizeOf(f), "byte", "bytes"), etagOf(f))
}

func putStream(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("The row is committed pending before any byte is put; the store takes the body to its end, and the row is completed with the size and etag the store reports.")
		return put(ctx, inv.Get(st), r, filesArea+"/docs/hello.txt", files.Content{
			Body: strings.NewReader(hello), ContentType: "text/plain",
		})
	}
}

func putSized(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		return put(ctx, inv.Get(st), r, filesArea+"/docs/notes.txt", files.Content{
			Body: strings.NewReader(notes), Size: int64(len(notes)), ContentType: "text/plain",
		})
	}
}

// catFile shows the content of the available file at path, and checks it
// is want, the bytes the tour put.
func catFile(ctx context.Context, st *files.Storage, r *Reporter, path, want string) error {
	body, _, err := st.Open(ctx, files.Ref{Path: path})
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("cat %s: %w", path, err)
	}
	if string(b) != want {
		return fmt.Errorf("cat %s read %q, want the %q the tour put", path, b, want)
	}
	return r.Show(func(w io.Writer) error {
		_, err := w.Write(b)
		return err
	})
}

func catHello(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		return catFile(ctx, inv.Get(st), r, filesArea+"/docs/hello.txt", hello)
	}
}

func copyHello(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("A destination that names an existing directory takes the copy under the source's name; the copy is a new row over a new object.")
		s := inv.Get(st)
		res, err := s.Copy(ctx, files.Ref{Path: filesArea + "/docs/hello.txt"}, files.Ref{Path: filesArea})
		if err != nil {
			return err
		}
		if err := r.showf("cp: %s -> %s (id %s, %s, etag %s)\n", res.From, res.To, res.File.ID, output.Count(sizeOf(res.File), "byte", "bytes"), etagOf(res.File)); err != nil {
			return err
		}
		return catFile(ctx, s, r, res.To, hello)
	}
}

func listFilesArea(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		s := inv.Get(svc)
		l := files.Listing{Page: 1, Size: 20}
		for _, path := range []string{filesArea, filesArea + "/docs"} {
			c, err := s.List(ctx, files.Ref{Path: path}, l)
			if err != nil {
				return err
			}
			r.Note("ls %s", path)
			if err := showListing(r, l, c); err != nil {
				return err
			}
		}
		return nil
	}
}

func removeCopy(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		path := filesArea + "/hello.txt"
		f, err := inv.Get(st).Remove(ctx, files.Ref{Path: path})
		if err != nil {
			return err
		}
		return r.showf("rm: %s (id %s)\n", f.Path, f.Row.ID)
	}
}

func removeFilesTree(st *graph.Node[*files.Storage]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("The branch is marked deleting in one transaction, then swept until no work remains: each file's object and row, then each directory once it is empty. A rerun starts clean.")
		return removeFilesArea(ctx, inv.Get(st), r)
	}
}

// removeFilesArea removes the working area with rm --recursive and shows
// its totals.
func removeFilesArea(ctx context.Context, st *files.Storage, r *Reporter) error {
	res, err := st.RemoveTree(ctx, files.Ref{Path: filesArea})
	if err != nil {
		return err
	}
	return r.showf("rm --recursive: %s (%s, %s)\n", filesArea, output.Count(res.Files, "file", "files"), output.Count(res.Directories, "directory", "directories"))
}
