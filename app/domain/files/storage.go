package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/standards-lab/blobfs"
	bfdata "github.com/standards-lab/blobfs/data"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/sqlate"

	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// This file holds the Storage and the blob protocol: the object operations,
// Put, Open, Copy, Remove, and RemoveTree, composed from blobfs's protocols
// over the database and the object store. Every write runs blobfs's
// two-phase write and every delete its two-phase delete, so the steps,
// their transactions, and their cleanup are the ones blobfs's conformance
// suite proves; the domain adds the path resolution and the refusals of its
// own. It holds the adapter that is blobfs's ObjectStore over go-storage's
// Store.

// Storage is the domain's second API, over the database and the object
// store: the [Service]'s data access, which it shares, and the object
// store blobfs's protocols put to, read from, and delete from. Its methods
// are the object operations. Only the commands that touch objects run on
// it, so only they declare the object store, and the Service's commands
// never build it.
type Storage struct {
	store   *store
	objects objectStore
}

// NewStorage returns the object operations over svc's data access and st,
// the started object store. It does no I/O.
func NewStorage(svc *Service, st *storage.Store) *Storage {
	return &Storage{store: svc.store, objects: objectStore{st}}
}

// objectStore is the object store as blobfs's protocols call it, its
// bfdata.ObjectStore: the key check, the put, and the delete over
// go-storage's Store, and the read cat and cp make.
type objectStore struct {
	store *storage.Store
}

// ValidateKey reports whether the store's provider accepts key, by the
// provider's own rule.
func (o objectStore) ValidateKey(key string) error {
	if validate := o.store.Capabilities().ValidateKey; validate != nil {
		return validate(key)
	}
	return nil
}

// PutObject stores size bytes of body under key in contentType, or the
// whole body when size is 0, and reports the stored object as blobfs
// records it.
func (o objectStore) PutObject(ctx context.Context, key string, body io.Reader, contentType string, size int64) (blobfs.Object, error) {
	obj, err := o.store.Put(ctx, key, body, storage.PutOptions{ContentType: contentType, Size: size})
	if err != nil {
		return blobfs.Object{}, err
	}
	return blobfs.Object{Size: obj.Size, ContentType: obj.ContentType, ETag: obj.ETag}, nil
}

// DeleteObject removes the object under key. go-storage's Delete treats a
// missing object as success, as blobfs's protocols require of it.
func (o objectStore) DeleteObject(ctx context.Context, key string) error {
	return o.store.Delete(ctx, key)
}

// open opens the object under key for reading. The caller closes it.
func (o objectStore) open(ctx context.Context, key string) (io.ReadCloser, error) {
	blob, err := o.store.Get(ctx, key, storage.GetOptions{})
	if err != nil {
		return nil, err
	}
	return blob.Body, nil
}

// Put writes c as a new file and returns the row available with its path:
// the file at dst's path, whose parent must exist, or the file named c.Name
// in the directory with dst's id, whose path blobfs computes before the
// write. The parent is resolved on the pool. The root is
// blobfs.ErrRootDirectory and a relative path blobfs.ErrInvalidPath, and a
// directory id with no c.Name a [FormError], all before any I/O; a
// directory that does not exist is blobfs.ErrNotFound.
//
// The name is looked up first. A pending row that holds it, which a put
// that stopped after its first step left, is resumed: blobfs's EnsureFile
// runs under the row's own id, so Files.Ensure finds the row, and the
// object is put under its key in the content type it declared, and the row
// completed. Any other put runs blobfs's WriteFile, whose Files.Create
// commits the pending row before any byte is put; a name an available row
// holds is blobfs.ErrNameTaken, and one a deleting row holds that row's
// blobfs.DeletingError, both before the store is reached. A put or a
// completion that fails abandons the write, so the name is free for a
// retry.
func (s *Storage) Put(ctx context.Context, dst Ref, c Content) (PutResult, error) {
	at := dst.Path
	if dst.ID != "" {
		at = c.Name + " in directory " + dst.ID
	}
	res, err := s.put(ctx, dst, c)
	if err != nil {
		return PutResult{}, fmt.Errorf("files: put %s: %w", at, err)
	}
	return res, nil
}

// put is Put's body, its errors unlabelled.
func (s *Storage) put(ctx context.Context, dst Ref, c Content) (PutResult, error) {
	if dst.ID != "" {
		if c.Name == "" {
			return PutResult{}, &FormError{Reason: "a file put into a directory by id takes a name"}
		}
		dir, err := s.store.blobfs.Directories.Path(ctx, s.store.db, dst.ID)
		if err != nil {
			return PutResult{}, err
		}
		res, err := s.write(ctx, dst.ID, c.Name, c)
		res.Path = join(dir, c.Name)
		return res, err
	}
	parent, name, err := splitParent(dst.Path)
	if err != nil {
		return PutResult{}, err
	}
	dir, err := s.store.resolve(ctx, s.store.db, parent)
	if err != nil {
		return PutResult{}, err
	}
	res, err := s.write(ctx, dir.ID, name, c)
	res.Path = dst.Path
	return res, err
}

// write writes c as the file name in the directory with directoryID.
func (s *Storage) write(ctx context.Context, directoryID, name string, c Content) (PutResult, error) {
	fs, db := s.store.blobfs, s.store.db
	held, err := fs.Files.FindByName(ctx, db, directoryID, name)
	switch {
	case err == nil && held.Status == blobfs.StatusPending:
		f, _, err := fs.EnsureFile(ctx, db, s.objects, held.ID, c.Body, c.Size, func(tx *sqlate.Tx) (blobfs.File, bfdata.WriteOutcome, error) {
			return fs.Files.Ensure(ctx, tx, s.objects, directoryID, name, c.ContentType, bfdata.WithID(held.ID))
		})
		if err != nil {
			return PutResult{}, err
		}
		return PutResult{File: f, Resumed: true}, nil
	case err != nil && !errors.Is(err, blobfs.ErrNotFound):
		return PutResult{}, err
	}
	f, err := fs.WriteFile(ctx, db, s.objects, c.Body, c.Size, func(tx *sqlate.Tx) (blobfs.File, error) {
		return fs.Files.Create(ctx, tx, s.objects, directoryID, name, c.ContentType)
	})
	if err != nil {
		return PutResult{}, err
	}
	return PutResult{File: f}, nil
}

// Open opens the content of the file ref names for reading and returns
// the row with its path, which for a file named by id blobfs computes from
// the row, so a caller reports a failed read at the path; the caller
// closes the reader. Only an available file has content: a pending or
// deleting one is ErrNotAvailable, before the store is reached.
func (s *Storage) Open(ctx context.Context, ref Ref) (io.ReadCloser, Located[blobfs.File], error) {
	at := label(ref, "file")
	f, err := s.store.file(ctx, s.store.db, ref)
	if err != nil {
		return nil, Located[blobfs.File]{}, fmt.Errorf("files: open %s: %w", at, err)
	}
	if err := available(f); err != nil {
		return nil, Located[blobfs.File]{}, fmt.Errorf("files: open %s: %w", at, err)
	}
	path, err := s.store.filePath(ctx, s.store.db, ref, f)
	if err != nil {
		return nil, Located[blobfs.File]{}, fmt.Errorf("files: open %s: %w", at, err)
	}
	body, err := s.objects.open(ctx, f.Key)
	if err != nil {
		return nil, Located[blobfs.File]{}, fmt.Errorf("files: open %s: %w", path, err)
	}
	return body, Located[blobfs.File]{Path: path, Row: f}, nil
}

// available refuses a file that has no content to read or copy: a pending
// file's object is not written yet, and a deleting file's is being
// removed.
func available(f blobfs.File) error {
	if f.Status != blobfs.StatusAvailable {
		return fmt.Errorf("%w: it is %s", ErrNotAvailable, f.Status)
	}
	return nil
}

// Copy copies the available file src names to a new file with its bytes
// and its content type. src and dst each take a path or an id, and each
// is resolved on its own, so a path and an id mix. A dst by path is read
// as Move reads it: an existing directory receives the copy under the
// source's name, and any other path is the copy's path, whose parent must
// exist. A dst by id is the directory that receives the copy under the
// source's name. The path of a source by id, and of a destination by id,
// is computed before the copy, so the result reports both paths whichever
// form named them. The source and the destination are read on the pool.
//
// The copy is blobfs's WriteFile: Files.Create commits the copy's pending
// row in the source's content type, which refuses a name a pending or
// available file holds as blobfs.ErrNameTaken, so nothing is overwritten;
// the source's object is then opened, only once the row is committed, and
// streamed through this process under the copy's key, and the row
// completed.
//
// The root as src is blobfs.ErrRootDirectory and a relative dst
// blobfs.ErrInvalidPath, both before any I/O. A source that does not
// exist, or a destination directory that does not, is blobfs.ErrNotFound,
// before any row is written; a source that is not available is
// ErrNotAvailable.
func (s *Storage) Copy(ctx context.Context, src, dst Ref) (CopyResult, error) {
	at := label(src, "file") + " into " + label(dst, "directory")
	if dst.ID == "" && !strings.HasPrefix(dst.Path, "/") {
		return CopyResult{}, fmt.Errorf("files: copy %s: %w: %q does not start with /", at, blobfs.ErrInvalidPath, dst.Path)
	}
	res, err := s.copyRef(ctx, src, dst, &at)
	if err != nil {
		return CopyResult{}, fmt.Errorf("files: copy %s: %w", at, err)
	}
	return res, nil
}

// copyRef is Copy's body, its errors unlabelled: the source read and
// checked available, its path, the destination's directory, its path, and
// the copy's name found, and the copy written. It rewrites *at, the label
// Copy gives an error, as each side resolves: the source's path once it is
// computed, and then the destination directory's path, with the copy's
// name after "as" when it is not the source's, so an error after the
// resolution names the paths, not the arguments.
func (s *Storage) copyRef(ctx context.Context, src, dst Ref, at *string) (CopyResult, error) {
	db := s.store.db
	f, err := s.store.file(ctx, db, src)
	if err != nil {
		return CopyResult{}, err
	}
	if err := available(f); err != nil {
		return CopyResult{}, err
	}
	from, err := s.store.filePath(ctx, db, src, f)
	if err != nil {
		return CopyResult{}, err
	}
	*at = from + " into " + label(dst, "directory")
	dirID, toDir, name, err := s.store.destination(ctx, db, dst, f.Name)
	if err != nil {
		return CopyResult{}, err
	}
	*at = from + " into " + toDir
	if name != f.Name {
		*at += " as " + name
	}
	made, err := s.copy(ctx, f, dirID, name)
	if err != nil {
		return CopyResult{}, err
	}
	return CopyResult{From: from, To: join(toDir, made.Name), File: made}, nil
}

// copy writes src's object as the new file name in the directory with
// directoryID, through blobfs's WriteFile. The body opens src's object on
// its first read, which WriteFile makes only after the pending row
// commits, so a refused name never reaches the store.
func (s *Storage) copy(ctx context.Context, src blobfs.File, directoryID, name string) (blobfs.File, error) {
	body := &deferredBody{open: func() (io.ReadCloser, error) { return s.objects.open(ctx, src.Key) }}
	defer body.close()
	var size int64
	if src.Size != nil {
		size = *src.Size
	}
	fs := s.store.blobfs
	return fs.WriteFile(ctx, s.store.db, s.objects, body, size, func(tx *sqlate.Tx) (blobfs.File, error) {
		return fs.Files.Create(ctx, tx, s.objects, directoryID, name, src.ContentType)
	})
}

// deferredBody is a body that opens its reader on the first Read.
type deferredBody struct {
	open func() (io.ReadCloser, error)
	rc   io.ReadCloser
}

func (b *deferredBody) Read(p []byte) (int, error) {
	if b.rc == nil {
		rc, err := b.open()
		if err != nil {
			return 0, err
		}
		b.rc = rc
	}
	return b.rc.Read(p)
}

// close closes the reader if one was opened. Its error is dropped: the
// body was read to the end or the write already failed.
func (b *deferredBody) close() {
	if b.rc != nil {
		_ = b.rc.Close()
	}
}

// Remove deletes the file ref names, whatever its status, and returns its
// row as the delete found it with its path, which for a file named by id is
// computed in the delete's first transaction. It is blobfs's RemoveFile:
// the file's lookup, the domain's check that the file may be removed, and
// blobfs's Files.Delete run in one transaction, which commits the row
// deleting; the object is then deleted and the row purged. A file a unit
// has bookmarked is refused with ErrBookmarked in that transaction, before
// anything is touched. A delete that stopped after its first step left the
// row deleting, and a later Remove finishes it. The root is
// blobfs.ErrRootDirectory before any I/O, and a file that does not exist is
// blobfs.ErrNotFound.
func (s *Storage) Remove(ctx context.Context, ref Ref) (Located[blobfs.File], error) {
	if err := checkRoot(ref); err != nil {
		return Located[blobfs.File]{}, fmt.Errorf("files: remove %s: %w", label(ref, "file"), err)
	}
	var path string
	f, err := s.remove(ctx, func(tx *sqlate.Tx) (blobfs.File, error) {
		f, err := s.store.file(ctx, tx, ref)
		if err != nil {
			return blobfs.File{}, err
		}
		path, err = s.store.filePath(ctx, tx, ref, f)
		return f, err
	})
	if err != nil {
		return Located[blobfs.File]{}, fmt.Errorf("files: remove %s: %w", label(ref, "file"), err)
	}
	return Located[blobfs.File]{Path: path, Row: f}, nil
}

// remove runs blobfs's RemoveFile of the file find reads in the delete's
// transaction, after the domain's removable check, and returns the row
// find read.
func (s *Storage) remove(ctx context.Context, find func(*sqlate.Tx) (blobfs.File, error)) (blobfs.File, error) {
	var f blobfs.File
	err := s.store.blobfs.RemoveFile(ctx, s.store.db, s.objects, func(tx *sqlate.Tx) (string, error) {
		var err error
		if f, err = find(tx); err != nil {
			return "", err
		}
		if err := s.removable(ctx, tx, f); err != nil {
			return "", err
		}
		return f.ID, nil
	})
	return f, err
}

// removable is the check a file's delete runs in its first transaction,
// after the file is read and before anything is touched: a file a unit has
// bookmarked is refused with ErrBookmarked, which rolls the transaction
// back with the row and the object as they were.
//
// blobfs's RemoveFile runs this check before its Files.Delete, whose
// update would take the file's row lock, so the check takes the lock
// itself first, with Files.Hold: add bookmark holds the row before it
// inserts, so an add that held first has committed its bookmark before the
// count reads, and one that arrives later waits for this transaction and
// then refuses the deleting row. A row deleting already, which an earlier
// remove left, cannot be held and needs no hold, since no add can reach it; its
// bookmarks are counted all the same.
func (s *Storage) removable(ctx context.Context, tx *sqlate.Tx, f blobfs.File) error {
	if err := s.store.blobfs.Files.Hold(ctx, tx, f.ID); err != nil && !errors.Is(err, blobfs.ErrDeleting) {
		return err
	}
	n, err := s.store.bookmarksOfFile(ctx, tx, f.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return &bookmarkedError{count: n}
	}
	return nil
}

// RemoveTree deletes the directory ref names, by path or by id, and
// everything beneath it, by blobfs's branch delete:
// Directories.MarkDeleting marks the branch deleting in one transaction,
// after which the branch takes nothing new, and blobfs's sweep then runs
// passes until no work remains, deleting each file's object and purging its
// row, and removing each directory once it is empty. The directory is read
// on the pool, and for a branch named by id its path computed, before the
// mark. Each directory's owner row is removed in the transaction that
// removes the directory, through the sweep's OnRemoveDirectory hook, so an
// owned top-level directory goes with its owner row. The result carries the
// branch's path and counts what the passes removed.
//
// A branch that holds a file a unit has bookmarked is refused with
// ErrBookmarked in the mark's transaction, after the mark and before it
// commits, so the refusal rolls the mark back and nothing is touched. The
// mark takes every file's row lock, as a file's delete does, so it waits
// on a bookmark add's hold, and the count after it sees every bookmark a
// hold admitted; once the branch is marked, no add can hold its files.
//
// The sweep finishes every marked branch, not only this one, so a branch
// an earlier RemoveTree marked and did not finish, because it was
// interrupted, is finished too, and a RemoveTree of the same path, whose
// directory is still found while it is deleting, marks it again and
// finishes it. A pass's refusals, such as an object the store would not
// delete, leave their rows for a later run; the error of the last pass is
// returned with the counts. The root is blobfs.ErrRootDirectory before any
// I/O, by path or by the root's id.
func (s *Storage) RemoveTree(ctx context.Context, ref Ref) (TreeRemoval, error) {
	at := label(ref, "directory")
	if err := checkRoot(ref); err != nil {
		return TreeRemoval{}, fmt.Errorf("files: remove tree %s: %w", at, err)
	}
	fs, db := s.store.blobfs, s.store.db
	dir, err := s.store.directory(ctx, db, ref)
	if err != nil {
		return TreeRemoval{}, fmt.Errorf("files: remove tree %s: %w", at, err)
	}
	var removed TreeRemoval
	if removed.Path, err = s.store.directoryPath(ctx, db, ref, dir); err != nil {
		return TreeRemoval{}, fmt.Errorf("files: remove tree %s: %w", at, err)
	}
	_, err = db.Transact(ctx, func(tx *sqlate.Tx) (bfdata.Marked, error) {
		marked, err := fs.Directories.MarkDeleting(ctx, tx, dir.ID)
		if err != nil {
			return bfdata.Marked{}, err
		}
		n, err := s.store.bookmarksInBranch(ctx, tx, dir.ID)
		if err != nil {
			return bfdata.Marked{}, err
		}
		if n > 0 {
			return bfdata.Marked{}, &bookmarkedError{count: n, branch: true}
		}
		return marked, nil
	})
	if err != nil {
		return TreeRemoval{}, fmt.Errorf("files: remove tree %s: %w", at, err)
	}
	removeOwner := bfdata.OnRemoveDirectory(func(ctx context.Context, tx *sqlate.Tx, dir blobfs.Directory) error {
		return s.store.deleteOwner(ctx, tx, dir.ID)
	})
	var last error
	err = bfdata.SweepUntilDone(ctx, nil,
		func(ctx context.Context) (bfdata.SweepResult, error) {
			return fs.Sweep(ctx, db, s.objects, removeOwner)
		},
		func(res bfdata.SweepResult, err error) {
			removed.Files += res.Files
			removed.Directories += res.Directories
			last = err
		})
	if err == nil {
		err = last
	}
	if err != nil {
		return removed, fmt.Errorf("files: remove tree %s: removed %s and %s, then: %w", at, output.Count(removed.Files, "file", "files"), output.Count(removed.Directories, "directory", "directories"), err)
	}
	return removed, nil
}
