package scenario

import (
	"context"
	"errors"
	"fmt"

	"github.com/standards-lab/blobfs"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// directoriesArea is the directories tour's working area: a top-level
// directory the tour's unit owns, which the tour creates, works under, and
// removes, and clears first when an earlier run left it behind.
const directoriesArea = "/scenario-directories"

// unit is the unit the directories tour creates its working area as, a
// fixed UUID, so a rerun lists the same unit's directories.
const unit = "0199c0de-0000-7000-8000-0000000000de"

// children are the directories the tour creates under its working area,
// in creation order.
var children = []string{"alpha", "bravo", "charlie", "delta", "echo"}

// action is a step's action, as a tour's step functions return it.
type action = func(ctx context.Context, inv *cli.Invocation, r *Reporter) error

// directoriesTour is the directories tour over svc, the files node: mkdir,
// ls with its paging, sorting, and filtering, stat, mv, and rmdir, with
// the working area created and listed as a unit's. It declares the files
// node alone, so a run builds Postgres and never the object store, and it
// runs with the store unreachable. Each step closes over svc and reads the
// Service with inv.Get.
func directoriesTour(svc *graph.Node[*files.Service]) Scenario {
	return Scenario{
		Name:    "directories",
		Summary: "Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir",
		Nodes:   []graph.Ref{svc},
		Steps: []Step{
			{Intent: "Clear " + directoriesArea + " if an earlier run left it behind", Action: clearDirectories(svc)},
			{Intent: "Create " + directoriesArea + " as the scenario unit's: mkdir --unit", Action: mkdirArea(svc)},
			{Intent: "List the unit's top-level directories: ls / --unit", Action: listUnit(svc)},
			{Intent: "Create five directories under the working area: mkdir", Action: mkdirChildren(svc)},
			{Intent: "List the first page of two, by name descending: ls --size 2 --sort name:desc", Action: listFirstPage(svc)},
			{Intent: "Continue after the page's cursor: ls --after-dirs", Action: listNextPage(svc)},
			{Intent: "Filter by name: ls --filter name:in:alpha,charlie,echo", Action: listFiltered(svc)},
			{Intent: "Show one directory's row: stat", Action: stat(svc)},
			{Intent: "Move echo into alpha, then rename bravo to foxtrot: mv", Action: move(svc)},
			{Intent: "List the working area and alpha after the moves: ls", Action: listMoved(svc)},
			{Intent: "Remove the working area, deepest directory first: rmdir", Action: removeDirectoriesArea(svc)},
		},
	}
}

func clearDirectories(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		s := inv.Get(svc)
		_, err := s.Resolve(ctx, files.Ref{Path: directoriesArea})
		if errors.Is(err, blobfs.ErrNotFound) {
			r.Note("Nothing to clear: %s does not exist, so this run starts clean.", directoriesArea)
			return nil
		}
		if err != nil {
			return err
		}
		r.Note("An earlier run stopped before its last step and left %s behind; it is removed, deepest directory first, so this run starts clean.", directoriesArea)
		return removeDirectories(ctx, s, r, directoriesArea)
	}
}

func mkdirArea(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("A directory created with a unit is top-level, and its owner row is written in the same transaction, so the unit's scope starts here.")
		dir, err := inv.Get(svc).Mkdir(ctx, files.Ref{Path: directoriesArea}, unit)
		if err != nil {
			return err
		}
		return r.showf("mkdir: %s (id %s, unit %s)\n", directoriesArea, dir.ID, unit)
	}
}

func listUnit(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("At the root, a unit's listing is its own top-level directories, read through the owner rows.")
		l := files.Listing{Page: 1, Size: 20, Unit: unit}
		c, err := inv.Get(svc).List(ctx, files.Ref{Path: "/"}, l)
		if err != nil {
			return err
		}
		return showListing(r, l, c)
	}
}

func mkdirChildren(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		s := inv.Get(svc)
		for _, name := range children {
			path := directoriesArea + "/" + name
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

// firstPage is the listing of the paging steps: two rows a page, by name
// descending.
func firstPage() files.Listing {
	return files.Listing{Page: 1, Size: 2, Sort: []files.Sort{{Field: "name", Descending: true}}}
}

func listFirstPage(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("A page by number counts the total; the next-dirs line is the cursor that continues the directory half.")
		l := firstPage()
		c, err := inv.Get(svc).List(ctx, files.Ref{Path: directoriesArea}, l)
		if err != nil {
			return err
		}
		return showListing(r, l, c)
	}
}

func listNextPage(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("The cursor continues after the last row the first page showed, under the same sort; a continued half counts nothing.")
		l := firstPage()
		s := inv.Get(svc)
		first, err := s.List(ctx, files.Ref{Path: directoriesArea}, l)
		if err != nil {
			return err
		}
		if first.Directories.Next == "" {
			return fmt.Errorf("the first page of %s has no cursor to continue after", directoriesArea)
		}
		l.After = files.After{Directories: first.Directories.Next}
		c, err := s.List(ctx, files.Ref{Path: directoriesArea}, l)
		if err != nil {
			return err
		}
		return showListing(r, l, c)
	}
}

func listFiltered(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		l := files.Listing{Page: 1, Size: 20, Filters: []files.Filter{
			{Field: "name", Op: "in", Value: []any{"alpha", "charlie", "echo"}},
		}}
		c, err := inv.Get(svc).List(ctx, files.Ref{Path: directoriesArea}, l)
		if err != nil {
			return err
		}
		return showListing(r, l, c)
	}
}

func stat(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		path := directoriesArea + "/alpha"
		dir, err := inv.Get(svc).Resolve(ctx, files.Ref{Path: path})
		if err != nil {
			return err
		}
		return showDirectory(r, path, dir)
	}
}

func move(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("A destination that names an existing directory takes the source under its own name; one that names a new path renames. Both stay under the one top-level directory.")
		s := inv.Get(svc)
		for _, mv := range [][2]string{
			{directoriesArea + "/echo", directoriesArea + "/alpha"},
			{directoriesArea + "/bravo", directoriesArea + "/foxtrot"},
		} {
			res, err := s.Move(ctx, files.Ref{Path: mv[0]}, files.Ref{Path: mv[1]})
			if err != nil {
				return err
			}
			if err := r.showf("mv: %s -> %s (id %s)\n", res.From, res.To, res.ID); err != nil {
				return err
			}
		}
		return nil
	}
}

func listMoved(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		s := inv.Get(svc)
		l := files.Listing{Page: 1, Size: 20}
		for _, path := range []string{directoriesArea, directoriesArea + "/alpha"} {
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

func removeDirectoriesArea(svc *graph.Node[*files.Service]) action {
	return func(ctx context.Context, inv *cli.Invocation, r *Reporter) error {
		r.Note("rmdir removes an empty directory only, so the tree goes from the leaves up; the working area's owner row goes with it, and a rerun starts clean.")
		return removeDirectories(ctx, inv.Get(svc), r, directoriesArea)
	}
}

// removeDirectories removes the directory at path and every directory
// under it with rmdir, deepest first, showing each removal. A directory
// that holds a file is refused by rmdir, and the refusal is returned: the
// directories tour creates none.
func removeDirectories(ctx context.Context, s *files.Service, r *Reporter, path string) error {
	l := files.Listing{Page: 1, Size: 100, Total: files.TotalNone}
	for {
		c, err := s.List(ctx, files.Ref{Path: path}, l)
		if err != nil {
			return err
		}
		if len(c.Directories.Rows) == 0 {
			break
		}
		for _, sub := range c.Directories.Rows {
			if err := removeDirectories(ctx, s, r, path+"/"+sub.Name); err != nil {
				return err
			}
		}
	}
	dir, err := s.RemoveDirectory(ctx, files.Ref{Path: path})
	if err != nil {
		return err
	}
	return r.showf("rmdir: %s (id %s)\n", path, dir.Row.ID)
}
