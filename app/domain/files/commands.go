package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"uuid"

	"github.com/standards-lab/blobfs"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// Commands builds the domain's whole command surface over the composition
// root's two nodes: svc, the [Service]'s, and st, the [Storage]'s. Each
// command declares the one node it reads with Use, the bookmark
// subcommands through their parent, so the dispatcher builds and starts
// only what that command needs before the body runs, and shuts it down
// after; the body reads the value with the Invocation's Get. The directory
// commands, mkdir, ls, stat, mv, and rmdir, and the bookmark command with
// add, ls, and rm, declare svc, so they build the database and the
// Service's statement check and never the object store. The object
// commands, put, cat, cp, and rm, declare st, so they build the object
// store too, and a store that cannot be reached fails them at start,
// naming the store's node, before anything is read or written.
//
// Each command counts its arguments in Args and checks them and its flags
// in Validate, the domain's request rules included, so a malformed
// path-or-id, an id where no id can name the target, the root where an
// entry is removed or bookmarked, a unit, a unit below the top level, a
// cursor into ls / --unit, a filter, a sort term, or a total mode is a
// usage error before anything is built; each bookmark
// subcommand requires --unit, so a run without it is a usage error too.
func Commands(svc *graph.Node[*Service], st *graph.Node[*Storage]) []*cli.Command {
	return []*cli.Command{
		mkdir(svc),
		ls(svc),
		stat(svc),
		mv(svc),
		rmdir(svc),
		bookmark(svc),
		put(st),
		cat(st),
		cp(st),
		rm(st),
	}
}

// mkdir is mkdir <path> [--unit <uuid>]: the last segment created under
// its existing parent, and with --unit, at a top-level path, the owner row
// that binds it to the unit, in the same transaction. There is no -p; a
// missing parent is an error.
func mkdir(svc *graph.Node[*Service]) *cli.Command {
	var unit string
	var ref Ref
	cmd := &cli.Command{
		Name:     "mkdir",
		Summary:  "Create a directory under an existing parent",
		Synopsis: "<path>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			if err := checkMkdir(ref); err != nil {
				return err
			}
			if unit, err = parseUnit(unit); err != nil {
				return err
			}
			if err := checkUnitDepth(ref, unit); err != nil {
				return fmt.Errorf("%w; give --unit with a top-level path only", err)
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			dir, err := inv.Get(svc).Mkdir(ctx, ref, unit)
			if err != nil {
				return err
			}
			if unit != "" {
				_, err := fmt.Fprintf(inv.Stdout, "mkdir: %s (id %s, unit %s)\n", ref.Path, dir.ID, unit)
				return err
			}
			_, err = fmt.Fprintf(inv.Stdout, "mkdir: %s (id %s)\n", ref.Path, dir.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&unit, "unit", "", "the id of the unit that owns the directory, a UUID; top-level paths only")
	return cmd.Use(svc)
}

// ls is ls <path|id:<uuid>>: the directories under the directory first,
// then its files, each one page, with a line per half stating the page and
// the total, and the cursor lines only under --cursors. With --unit the
// unit must own the directory's top-level ancestor, whether the path or
// the id names it, and ls / lists the unit's own top-level directories.
func ls(svc *graph.Node[*Service]) *cli.Command {
	var f listingFlags
	var ref Ref
	var l Listing
	cmd := &cli.Command{
		Name:     "ls",
		Summary:  "List a directory: its directories, then its files, one page each",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			if l, err = f.listing(); err != nil {
				return err
			}
			if err := checkRootCursor(ref, l); err != nil {
				return fmt.Errorf("%w; ls / --unit takes no --after-dirs or --after-files", err)
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			c, err := inv.Get(svc).List(ctx, ref, l)
			if err != nil {
				return err
			}
			return WriteContents(inv.Stdout, l, c, f.cursors)
		},
	}
	f.bind(cmd)
	return cmd.Use(svc)
}

// stat is stat <path|id:<uuid>>: the file's row, or the directory's when
// no file is at the path or has the id, one field per line, led by its
// path, which for an id is the path the stat resolved.
func stat(svc *graph.Node[*Service]) *cli.Command {
	var ref Ref
	return (&cli.Command{
		Name:     "stat",
		Summary:  "Show a file's or a directory's row, one field per line",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			ref, err = parseRef(inv.Args[0])
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			e, err := inv.Get(svc).Stat(ctx, ref)
			if err != nil {
				return err
			}
			if e.Kind == EntryFile {
				return writeFileRecord(inv.Stdout, e.Path, e.File)
			}
			return WriteDirectoryRecord(inv.Stdout, e.Path, e.Directory)
		},
	}).Use(svc)
}

// mv is mv <path|id:<uuid>> <path|id:<uuid>>, src then dst: the directory or file src names moved, in one
// transaction, each argument a path or an id. A src by id is the file with
// it, or else the directory. A dst by path is an existing directory to
// move into, or else the new path; a dst by id is the directory to move
// into, keeping the name. The success line names both resolved paths.
func mv(svc *graph.Node[*Service]) *cli.Command {
	var src, dst Ref
	return (&cli.Command{
		Name:     "mv",
		Summary:  "Move or rename a directory or a file within its top-level directory",
		Synopsis: "<path|id:<uuid>> <path|id:<uuid>>",
		Args:     cli.ExactArgs(2),
		Validate: func(inv *cli.Invocation) error {
			var err error
			src, dst, err = parsePair(inv.Args)
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			res, err := inv.Get(svc).Move(ctx, src, dst)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(inv.Stdout, "mv: %s -> %s (id %s)\n", res.From, res.To, res.ID)
			return err
		},
	}).Use(svc)
}

// rmdir is rmdir <path|id:<uuid>>: an empty directory removed, reported
// at its path, which for an id is the path the removal resolved. A
// directory that still has contents is refused, and so is the root, in
// Validate.
func rmdir(svc *graph.Node[*Service]) *cli.Command {
	var ref Ref
	return (&cli.Command{
		Name:     "rmdir",
		Summary:  "Remove an empty directory",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			if err := checkRoot(ref); err != nil {
				return fmt.Errorf("%w; rmdir removes a directory below the root", err)
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			dir, err := inv.Get(svc).RemoveDirectory(ctx, ref)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(inv.Stdout, "rmdir: %s (id %s)\n", dir.Path, dir.Row.ID)
			return err
		},
	}).Use(svc)
}

// bookmark is the bookmark command: add, ls, and rm, each under the unit
// its required --unit names. The parent declares svc for the three.
func bookmark(svc *graph.Node[*Service]) *cli.Command {
	return (&cli.Command{
		Name:    "bookmark",
		Summary: "Bookmark files for a unit, at most one of them active: add, ls, rm",
	}).Add(bookmarkAdd(svc), bookmarkLs(svc), bookmarkRm(svc)).Use(svc)
}

// bookmarkAdd is bookmark add <path|id:<uuid>> --unit <uuid> [--active]:
// the unit's bookmark of the file, active when asked, and refused while
// another bookmark of the unit is active. It reports the file at its
// path, which for an id is the path the add resolved. The root is
// refused in Validate.
func bookmarkAdd(svc *graph.Node[*Service]) *cli.Command {
	var unit string
	var active bool
	var ref Ref
	cmd := &cli.Command{
		Name:     "add",
		Summary:  "Bookmark a file for a unit; --active makes it the unit's one active bookmark",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			if err := checkRoot(ref); err != nil {
				return fmt.Errorf("%w; bookmark add takes a file", err)
			}
			unit, err = parseUnit(unit)
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			f, err := inv.Get(svc).AddBookmark(ctx, ref, unit, active)
			if err != nil {
				return err
			}
			state := "inactive"
			if active {
				state = "active"
			}
			_, err = fmt.Fprintf(inv.Stdout, "bookmark add: %s (file %s, unit %s, %s)\n", f.Path, f.Row.ID, unit, state)
			return err
		},
	}
	cmd.Flags().StringVar(&unit, "unit", "", "the id of the unit that bookmarks the file, a UUID")
	cmd.Flags().BoolVar(&active, "active", false, "make this bookmark the unit's one active bookmark; refused while another is active")
	cmd.Require("unit")
	return cmd
}

// bookmarkLs is bookmark ls --unit <uuid>: the unit's bookmarks with their
// files' full paths, one page by number, and a line stating the page and
// the total.
func bookmarkLs(svc *graph.Node[*Service]) *cli.Command {
	var f pageFlags
	var unit string
	var l Listing
	cmd := &cli.Command{
		Name:    "ls",
		Summary: "List a unit's bookmarks with their files' paths, one page",
		Args:    cli.NoArgs,
		Validate: func(*cli.Invocation) error {
			var err error
			if unit, err = parseUnit(unit); err != nil {
				return err
			}
			l, err = f.listing()
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			p, err := inv.Get(svc).ListBookmarks(ctx, unit, l)
			if err != nil {
				return err
			}
			return writeBookmarks(inv.Stdout, l, p)
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&unit, "unit", "", "the id of the unit whose bookmarks to list, a UUID")
	cmd.Require("unit")
	return cmd
}

// bookmarkRm is bookmark rm <path|id:<uuid>> --unit <uuid>: the unit's
// bookmark of the file removed, active or not, and the file reported at
// its path, which for an id is the path the removal resolved.
func bookmarkRm(svc *graph.Node[*Service]) *cli.Command {
	var unit string
	var ref Ref
	cmd := &cli.Command{
		Name:     "rm",
		Summary:  "Remove a unit's bookmark of a file, active or not",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			unit, err = parseUnit(unit)
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			f, err := inv.Get(svc).RemoveBookmark(ctx, ref, unit)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(inv.Stdout, "bookmark rm: %s (file %s, unit %s)\n", f.Path, f.Row.ID, unit)
			return err
		},
	}
	cmd.Flags().StringVar(&unit, "unit", "", "the id of the unit whose bookmark to remove, a UUID")
	cmd.Require("unit")
	return cmd
}

// put is put <local-file|-> <path|id:<uuid>> [--content-type <type>]: the
// local file, or standard input for -, written as the file at the path,
// or into the directory with the id under the local file's base name, and
// reported at the file's resolved path. Standard input has no name, so -
// takes a path.
func put(st *graph.Node[*Storage]) *cli.Command {
	var contentType string
	var dst Ref
	cmd := &cli.Command{
		Name:     "put",
		Summary:  "Upload a local file, or stdin for -, as the file at a path or into a directory",
		Synopsis: "<local-file|-> <path|id:<uuid>>",
		Args:     cli.ExactArgs(2),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if dst, err = parseRef(inv.Args[1]); err != nil {
				return err
			}
			if dst.ID != "" && inv.Args[0] == "-" {
				return errors.New("stdin has no name to store under; give the destination of - as a path")
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			src := inv.Args[0]
			body, size, closeBody, err := openLocal(ctx, inv.Stdin, src)
			if err != nil {
				return fmt.Errorf("files: put %s: %w", src, err)
			}
			defer closeBody()
			c := Content{Body: body, Size: size, ContentType: declaredType(contentType, src)}
			if dst.ID != "" {
				c.Name = filepath.Base(src)
			}
			res, err := inv.Get(st).Put(ctx, dst, c)
			if err != nil {
				return err
			}
			resumed := ""
			if res.Resumed {
				resumed = ", resumed the pending row"
			}
			f := res.File
			_, err = fmt.Fprintf(inv.Stdout, "put: %s (id %s, %s, etag %s%s)\n", res.Path, f.ID, output.Count(sizeOf(f), "byte", "bytes"), etagOf(f), resumed)
			return err
		},
	}
	cmd.Flags().StringVar(&contentType, "content-type", "", "the media type to store with the object; the default comes from the local file's extension, else application/octet-stream")
	return cmd.Use(st)
}

// cat is cat <path|id:<uuid>>: the available file's content streamed to
// stdout as it is. A read that fails midway names the file's resolved
// path.
func cat(st *graph.Node[*Storage]) *cli.Command {
	var ref Ref
	return (&cli.Command{
		Name:     "cat",
		Summary:  "Write an available file's content to stdout",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			ref, err = parseRef(inv.Args[0])
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			body, f, err := inv.Get(st).Open(ctx, ref)
			if err != nil {
				return err
			}
			defer func() { _ = body.Close() }()
			if _, err := io.Copy(inv.Stdout, body); err != nil {
				return fmt.Errorf("files: read %s: %w", f.Path, err)
			}
			return nil
		},
	}).Use(st)
}

// cp is cp <path|id:<uuid>> <path|id:<uuid>>, src then dst: the available file src names copied, each
// argument a path or an id. A dst by path is an existing directory to copy
// into under the source's name, or else the new path; a dst by id is the
// directory to copy into under the source's name. A name already taken is
// refused. The success line names both resolved paths.
func cp(st *graph.Node[*Storage]) *cli.Command {
	var src, dst Ref
	return (&cli.Command{
		Name:     "cp",
		Summary:  "Copy an available file into a directory or to a new path",
		Synopsis: "<path|id:<uuid>> <path|id:<uuid>>",
		Args:     cli.ExactArgs(2),
		Validate: func(inv *cli.Invocation) error {
			var err error
			src, dst, err = parsePair(inv.Args)
			return err
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			res, err := inv.Get(st).Copy(ctx, src, dst)
			if err != nil {
				return err
			}
			f := res.File
			_, err = fmt.Fprintf(inv.Stdout, "cp: %s -> %s (id %s, %s, etag %s)\n", res.From, res.To, f.ID, output.Count(sizeOf(f), "byte", "bytes"), etagOf(f))
			return err
		},
	}).Use(st)
}

// rm is rm <path|id:<uuid>>, a file deleted, and rm --recursive
// <path|id:<uuid>>, a directory and everything beneath it deleted with
// the totals its sweep removed; each is reported at the resolved path,
// which for an id is the path the delete computed. The root is refused,
// in either form, in Validate. A delete a bookmark refuses says so once,
// with the remedy. There is no -r shorthand.
func rm(st *graph.Node[*Storage]) *cli.Command {
	var recursive bool
	var ref Ref
	cmd := &cli.Command{
		Name:     "rm",
		Summary:  "Delete a file, or with --recursive a directory and everything beneath it",
		Synopsis: "<path|id:<uuid>>",
		Args:     cli.ExactArgs(1),
		Validate: func(inv *cli.Invocation) error {
			var err error
			if ref, err = parseRef(inv.Args[0]); err != nil {
				return err
			}
			if err := checkRoot(ref); err != nil {
				return fmt.Errorf("%w; rm removes a file, or with --recursive a directory, below the root", err)
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			s := inv.Get(st)
			if recursive {
				res, err := s.RemoveTree(ctx, ref)
				if errors.Is(err, ErrBookmarked) {
					return fmt.Errorf("%w; remove the bookmarks and rerun rm --recursive", err)
				}
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(inv.Stdout, "rm --recursive: %s (%s, %s)\n", res.Path, output.Count(res.Files, "file", "files"), output.Count(res.Directories, "directory", "directories"))
				return err
			}
			f, err := s.Remove(ctx, ref)
			if errors.Is(err, ErrBookmarked) {
				return fmt.Errorf("%w; remove the bookmarks and rerun rm", err)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(inv.Stdout, "rm: %s (id %s)\n", f.Path, f.Row.ID)
			return err
		},
	}
	cmd.Flags().BoolVar(&recursive, "recursive", false, "delete the directory the argument names and everything beneath it")
	return cmd.Use(st)
}

// openLocal opens the body put uploads: stdin for -, its length unknown,
// or the local file named, its length from the file system so the store
// holds the body to it. The close function releases what was opened.
func openLocal(ctx context.Context, stdin io.Reader, name string) (body io.Reader, size int64, closeBody func(), err error) {
	if name == "-" {
		body, closeBody := cancelable(ctx, stdin)
		return body, 0, closeBody, nil
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, 0, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, nil, err
	}
	return file, info.Size(), func() { _ = file.Close() }, nil
}

// cancelable returns r as a body whose Read fails with ctx's error once
// ctx ends. A read of the process's standard input blocks in the kernel,
// where no context reaches it, so an upload waiting on a pipe held open
// would outlive the signal that cancelled its run. The body is the read end
// of a pipe that a goroutine copies r into; ctx's end closes the pipe's
// write end with ctx's error, which unblocks a waiting Read. A copy still
// blocked reading r stays so until r yields or the process exits, and a
// write it makes after the close fails, ending it. The close function
// releases the context's hook and the pipe.
func cancelable(ctx context.Context, r io.Reader) (io.Reader, func()) {
	pr, pw := io.Pipe()
	go func() {
		_, err := io.Copy(pw, r)
		_ = pw.CloseWithError(err)
	}()
	stop := context.AfterFunc(ctx, func() { _ = pw.CloseWithError(ctx.Err()) })
	return pr, func() {
		stop()
		_ = pr.Close()
	}
}

// declaredType is the content type put declares: the flag when given,
// else the type the standard library registers for the local file's
// extension, else application/octet-stream, which is also stdin's.
func declaredType(flag, local string) string {
	if flag != "" {
		return flag
	}
	if local != "-" {
		if t := mime.TypeByExtension(filepath.Ext(local)); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

// The CLI-syntax parsers: each reads the command line's text into the
// domain's shapes, and each runs in a command's Validate, which reports
// any refusal as a usage error.

// parseRef reads one path-or-id argument. Text after an id: prefix must be
// a UUID other than the root's, checked by blobfs.ParseID, and is returned
// in canonical form; anything else is a path, which must pass checkPath:
// absolute, each segment a name blobfs takes. Neither check does I/O, so a
// relative or malformed argument is a usage error before anything is
// built.
func parseRef(arg string) (Ref, error) {
	rest, ok := strings.CutPrefix(arg, "id:")
	if !ok {
		if err := checkPath(arg); err != nil {
			return Ref{}, err
		}
		return Ref{Path: arg}, nil
	}
	id, err := blobfs.ParseID(rest)
	if err != nil {
		return Ref{}, err
	}
	return Ref{ID: id}, nil
}

// parsePair reads the two arguments of mv or cp, each a path or an id on
// its own.
func parsePair(args []string) (Ref, Ref, error) {
	src, err := parseRef(args[0])
	if err != nil {
		return Ref{}, Ref{}, err
	}
	dst, err := parseRef(args[1])
	if err != nil {
		return Ref{}, Ref{}, err
	}
	return src, dst, nil
}

// pageFlags is the flag set every paged listing takes: the page and its
// size, the repeatable sort term, and the total mode. bookmark ls takes it
// alone.
type pageFlags struct {
	page, size int
	sort       []string
	total      string
}

// bind defines the paging flags on cmd.
func (f *pageFlags) bind(cmd *cli.Command) {
	fs := cmd.Flags()
	fs.IntVar(&f.page, "page", 1, "the 1-based page to list")
	fs.IntVar(&f.size, "size", 20, "the number of rows per page")
	cli.StringsVar(fs, &f.sort, "sort", nil, "a sort term, <field> or <field>:desc, applied in order")
	fs.StringVar(&f.total, "total", "exact", "exact to count the total, none to omit it")
}

// listing builds the Listing the paging flags state, validating the total
// mode and parsing each sort term. It runs in Validate, which reports any
// refusal as a usage error.
func (f *pageFlags) listing() (Listing, error) {
	l := Listing{Page: f.page, Size: f.size}
	switch f.total {
	case "exact":
		l.Total = TotalExact
	case "none":
		l.Total = TotalNone
	default:
		return Listing{}, fmt.Errorf("--total %q: the mode is exact or none", f.total)
	}
	for _, term := range f.sort {
		s, err := parseSort(term)
		if err != nil {
			return Listing{}, err
		}
		l.Sort = append(l.Sort, s)
	}
	return l, nil
}

// listingFlags is the flag set ls takes: the paging flags, the repeatable
// filter term, the cursor of each half and whether to print the next ones,
// and the unit to list as.
type listingFlags struct {
	pageFlags
	filter                []string
	afterDirs, afterFiles string
	cursors               bool
	unit                  string
}

// bind defines the listing flags on cmd.
func (f *listingFlags) bind(cmd *cli.Command) {
	f.pageFlags.bind(cmd)
	fs := cmd.Flags()
	cli.StringsVar(fs, &f.filter, "filter", nil, "a filter term, <field>:<op>:<value>, or <field>:null and <field>:notnull")
	fs.StringVar(&f.afterDirs, "after-dirs", "", "continue the directory half after this cursor, from an earlier next-dirs: line")
	fs.StringVar(&f.afterFiles, "after-files", "", "continue the file half after this cursor, from an earlier next-files: line")
	fs.BoolVar(&f.cursors, "cursors", false, "print the next-dirs: and next-files: lines with the cursors that continue each half")
	fs.StringVar(&f.unit, "unit", "", "list as the unit with this id, a UUID; it must own the directory's top-level ancestor, and at / the listing is its own")
}

// listing builds the Listing the flags state, validating the unit and the
// total mode and parsing each filter and sort term. It runs in Validate,
// which reports any refusal as a usage error.
func (f *listingFlags) listing() (Listing, error) {
	unit, err := parseUnit(f.unit)
	if err != nil {
		return Listing{}, err
	}
	l, err := f.pageFlags.listing()
	if err != nil {
		return Listing{}, err
	}
	for _, term := range f.filter {
		filter, err := parseFilter(term)
		if err != nil {
			return Listing{}, err
		}
		l.Filters = append(l.Filters, filter)
	}
	l.After = After{Directories: f.afterDirs, Files: f.afterFiles}
	l.Unit = unit
	return l, nil
}

// parseUnit reads a --unit value: empty when the flag was not given, and
// otherwise a UUID, returned in canonical form; it runs in Validate, so a
// value that is not one is a usage error. A required --unit that is missing
// is reported by the dispatcher's Require, before Validate, so the empty
// value passes here only for an optional --unit.
func parseUnit(unit string) (string, error) {
	if unit == "" {
		return "", nil
	}
	id, err := uuid.Parse(unit)
	if err != nil {
		return "", fmt.Errorf("--unit %q is not a UUID", unit)
	}
	return id.String(), nil
}

// parseFilter reads one --filter term: <field>:<op>:<value>, where the
// value is the rest of the term and may hold colons, as a timestamp does;
// <field>:<op> alone for null and notnull; and for in, a value that is a
// comma-separated list. The field and the operator are checked by blobfs
// against the listing's declared fields and the query library's
// operators, so an unknown one is refused there, before the statement
// runs.
func parseFilter(term string) (Filter, error) {
	field, rest, ok := strings.Cut(term, ":")
	if field == "" || !ok {
		return Filter{}, fmt.Errorf("--filter %q: write <field>:<op>:<value>, or <field>:null or <field>:notnull", term)
	}
	op, value, hasValue := strings.Cut(rest, ":")
	if op == "" {
		return Filter{}, fmt.Errorf("--filter %q: names no operator; write <field>:<op>:<value>", term)
	}
	switch op {
	case "null", "notnull":
		if hasValue {
			return Filter{}, fmt.Errorf("--filter %q: %s takes no value; write %s:%s", term, op, field, op)
		}
		return Filter{Field: field, Op: op}, nil
	case "in":
		if !hasValue {
			return Filter{}, fmt.Errorf("--filter %q: in takes a comma-separated list; write %s:in:<value>,<value>", term, field)
		}
		parts := strings.Split(value, ",")
		values := make([]any, len(parts))
		for i, p := range parts {
			values[i] = p
		}
		return Filter{Field: field, Op: op, Value: values}, nil
	}
	if !hasValue {
		return Filter{}, fmt.Errorf("--filter %q: names no value; write <field>:<op>:<value>, or <field>:null or <field>:notnull", term)
	}
	return Filter{Field: field, Op: op, Value: value}, nil
}

// parseSort reads one --sort term: a field name, or a field name and :desc
// for descending order; :asc is accepted and means the default.
func parseSort(term string) (Sort, error) {
	field, direction, hasDirection := strings.Cut(term, ":")
	if field == "" {
		return Sort{}, fmt.Errorf("--sort %q: names no field; write <field> or <field>:desc", term)
	}
	switch {
	case !hasDirection, direction == "asc":
		return Sort{Field: field}, nil
	case direction == "desc":
		return Sort{Field: field, Descending: true}, nil
	}
	return Sort{}, fmt.Errorf("--sort %q: the direction is asc or desc", term)
}
