# Usage

A walkthrough of `blobfs` against the app's development stack, top to bottom, in about 20
minutes. Each block is copyable as written; where a command needs an id from earlier output, it
shows a placeholder such as `<alpha-id>`. Every output below is pasted from a real run; ids, etags,
and timestamps differ in yours. The outputs were captured in spike-cli-architecture, where the app
stored its objects in Azurite; a section whose output the S3 provider changes says so, and its
output is to be re-captured against SeaweedFS.

## What the app is

The app is the `blobfs` CLI of spike-cli-architecture, ported into this repository's `app` module
(`github.com/JaimeStill/spike-s3-storage/app`) with one change of substance: its object store is
this repository's `s3` provider against SeaweedFS's S3 gateway, where the source used go-storage's
`azureblob` against Azurite. It rebuilds spike-blobfs's CLI on a dispatcher over the standard
library's `flag` (package `cli`) and a typed dependency graph (package `graph`) run by a
Coordinator (package `lifecycle`). Each command declares the graph nodes it uses, and a run builds
and starts only those: help and usage errors open nothing, directory commands open Postgres, and
object commands open Postgres and the object store.

The `app` module requires no version of the `s3` module: it finds it through `go.work` alone, so it
builds only in workspace mode, from anywhere in the repository.

## Setup

The repository has no mise task that runs the binary, so build it into `bin/` (ignored by git)
and put it on the `PATH`. The `BLOBFS_*` variables come from `mise.toml`'s `[env]`, so the shell
needs mise activated in the repository; without it, prefix each command with `mise exec --`.

```sh
cd /path/to/spike-s3-storage
go build -o bin/blobfs ./app/cmd/blobfs
export PATH="$PWD/bin:$PATH"
```

Start Postgres (5438) and SeaweedFS's S3 gateway (8334), with the bucket `blobfs`, which the
store's start creates. `mise run app:reset` first drops any earlier data, so the walkthrough
starts from an empty database and an empty gateway:

```sh
mise run app:reset
mise run app:up
blobfs schema status
```

```
set     table                  version  latest  pending                                  dirty
blobfs  blobfs_schema_version  0        3       1 directory, 2 file, 3 directory_status  false
app     schema_version         0        2       1 directory_owner, 2 bookmark            false
```

A files command against the unapplied schema fails at start. The `files:` label is the graph
node whose `Start` checks every statement against the database, so the command's body never runs
(first lines of the report, which lists every statement; exit 1):

```sh
blobfs ls /
```

```
blobfs ls: files: the database does not satisfy the statements: the schema is not applied or does not match them: query: complete_file: ERROR: relation "blobfs_file" does not exist (SQLSTATE 42P01)
query: create_directory: ERROR: relation "blobfs_directory" does not exist (SQLSTATE 42P01)
query: create_file: ERROR: relation "blobfs_file" does not exist (SQLSTATE 42P01)
```

```sh
blobfs schema up       # schema up: both sets at head
blobfs schema status   # both sets at their latest version, pending none
```

## Help and the command surface

```sh
blobfs --help
```

```
blobfs manages files in a blob store.

Usage:
  blobfs <command> [flags]

Commands:
  version    Print the blobfs version
  schema     Report, apply, revert, and reset the two migration sets
  mkdir      Create a directory under an existing parent
  ls         List a directory: its directories, then its files, one page each
  stat       Show a file's or a directory's row, one field per line
  mv         Move or rename a directory or a file within its top-level directory
  rmdir      Remove an empty directory
  bookmark   Bookmark files for a unit, at most one of them active: add, ls, rm
  put        Upload a local file, or stdin for -, as the file at a path or into a directory
  cat        Write an available file's content to stdout
  cp         Copy an available file into a directory or to a new path
  rm         Delete a file, or with --recursive a directory and everything beneath it
  scenario   Run a narrated scenario over the commands

Flags:
  --help   Show help for blobfs

Run 'blobfs <command> --help' for help on a command.

A path argument may instead be id:<uuid>, naming the directory or file by its id.

Scenarios:
  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir
                uses files
  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive
                uses files, storage
```

The footer is the root command's `Footer`: the `id:` operand line, then the scenario listing with
the graph nodes each scenario declares. `blobfs scenario` alone prints its own help, which ends
with the same listing:

```sh
blobfs scenario
```

```
Run a narrated scenario over the commands

Usage:
  blobfs scenario <command> [flags]

Commands:
  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir
  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive

Flags:
  --help   Show help for blobfs scenario

Run 'blobfs scenario <command> --help' for help on a command.

Scenarios:
  directories   Tour the directory commands on Postgres alone: mkdir, ls, stat, mv, rmdir
                uses files
  files         Tour the object commands on Postgres and the store: put, cat, cp, rm, rm --recursive
                uses files, storage
```

A leaf's help lists its flags; a repeatable flag says so:

```sh
blobfs ls --help
```

```
List a directory: its directories, then its files, one page each

Usage:
  blobfs ls [flags] <path|id:<uuid>>

Flags:
  --after-dirs string    continue the directory half after this cursor, from an earlier next-dirs: line
  --after-files string   continue the file half after this cursor, from an earlier next-files: line
  --cursors              print the next-dirs: and next-files: lines with the cursors that continue each half
  --filter string        a filter term, <field>:<op>:<value>, or <field>:null and <field>:notnull (repeatable)
  --page int             the 1-based page to list (default 1)
  --size int             the number of rows per page (default 20)
  --sort string          a sort term, <field> or <field>:desc, applied in order (repeatable)
  --total string         exact to count the total, none to omit it (default "exact")
  --unit string          list as the unit with this id, a UUID; it must own the directory's top-level ancestor, and at / the listing is its own
  --help                 Show help for blobfs ls
```

Help and usage errors exit 2, through go-core's `process.Usage`; a command that fails exits 1.
Help goes to stdout, usage errors to stderr:

```sh
blobfs ls; echo $?
```

```
blobfs ls: accepts 1 argument, got 0
Usage: blobfs ls [flags] <path|id:<uuid>>
Run 'blobfs ls --help' for details.
2
```

`blobfs version` exits 0 and declares no nodes, so it builds nothing.

## Directories

Each directory command declares the `files` node alone, so it opens Postgres and never the
object store. Each success line names the entry by its resolved path and prints its id:

```sh
blobfs mkdir /projects
blobfs mkdir /projects/alpha
blobfs mkdir /projects/beta
blobfs mkdir /projects/gamma
blobfs mkdir /projects/delta
```

```
mkdir: /projects (id 01a11799-fb8d-70dc-8b77-9e17f1ad755a)
mkdir: /projects/alpha (id 01a11799-fba6-73eb-b7ed-bae1eec7775c)
mkdir: /projects/beta (id 01a11799-fbbb-7b3d-8ef4-7e9f2bbb4971)
mkdir: /projects/gamma (id 01a11799-fbd4-7cd8-98bd-b92fc9a7b66b)
mkdir: /projects/delta (id 01a11799-fbec-767f-bfc5-aed09c86877d)
```

The `(id …)` values are the `<alpha-id>`, `<beta-id>`, `<gamma-id>`, and `<delta-id>` used
below. To grab one later, read it from `stat`:

```sh
alpha_id=$(blobfs stat /projects/alpha | awk '/^id:/{print $2}')
```

### ls: pages, sort, filter, and totals

`ls` prints the directory half, then the file half, each with its own page line:

```sh
blobfs ls /projects --size 2
```

```
KIND  NAME   SIZE  STATUS  UPDATED              ID
dir   alpha  -     -       2026-10-07 18:22:09  01a11799-fba6-73eb-b7ed-bae1eec7775c
dir   beta   -     -       2026-10-07 18:22:09  01a11799-fbbb-7b3d-8ef4-7e9f2bbb4971
directories: 2 on page 1 of size 2, total 4
more: yes
files: 0 on page 1 of size 2, total 0
more: no
```

```sh
blobfs ls /projects --size 2 --page 2         # delta, gamma; the empty file half reads "total unknown (the page is empty)"
blobfs ls /projects --sort name:desc          # gamma, delta, beta, alpha
blobfs ls /projects --filter name:like:%e%    # beta, delta; total 2
blobfs ls /projects --size 2 --total none     # each half reads "total not counted"
```

### ls: cursors and continuation

`--cursors` adds a `next-dirs:` line, and a `next-files:` line when the file half has more:

```sh
blobfs ls /projects --size 2 --cursors
```

```
KIND  NAME   SIZE  STATUS  UPDATED              ID
dir   alpha  -     -       2026-10-07 18:22:09  01a11799-fba6-73eb-b7ed-bae1eec7775c
dir   beta   -     -       2026-10-07 18:22:09  01a11799-fbbb-7b3d-8ef4-7e9f2bbb4971
directories: 2 on page 1 of size 2, total 4
more: yes
next-dirs: Vff6NnsidiI6MSwiYmFzZSI6ImRpcmVjdG9yeV9jaGlsZHJlbiIsInRlcm1zIjpbIm5hbWUiXSwiZGVzYyI6ZmFsc2UsImZpbHRlcnMiOiJbe1wiRmllbGRcIjpcInN0YXR1c1wiLFwiT3BcIjpcIm5lXCIsXCJWYWx1ZVwiOlwiZGVsZXRpbmdcIn1dIiwidmFsdWVzIjpbImJldGEiXX0
files: 0 on page 1 of size 2, total 0
more: no
```

Pass the cursor back, with the same sort and filters, to continue that half. A continued half
counts nothing:

```sh
blobfs ls /projects --size 2 --after-dirs <next-dirs>
```

```
KIND  NAME   SIZE  STATUS  UPDATED              ID
dir   delta  -     -       2026-10-07 18:22:09  01a11799-fbec-767f-bfc5-aed09c86877d
dir   gamma  -     -       2026-10-07 18:22:09  01a11799-fbd4-7cd8-98bd-b92fc9a7b66b
directories: 2 after the cursor, size 2, total not counted
more: no
files: 0 on page 1 of size 2, total 0
more: no
```

`--after-files` continues the file half the same way; [Objects](#objects) shows both cursors
together once `/projects` holds files.

### stat

`stat` prints the `path:` line whichever form names the entry:

```sh
blobfs stat /projects/alpha
blobfs stat id:<alpha-id>
```

```
path:    /projects/alpha
id:      01a11799-fba6-73eb-b7ed-bae1eec7775c
parent:  01a11799-fb8d-70dc-8b77-9e17f1ad755a
name:    alpha
version: 1
created: 2026-10-07T18:22:09Z
updated: 2026-10-07T18:22:09Z
```

### mv

Each argument of `mv` is a path or an id on its own, so the forms mix. A destination that names
an existing directory takes the source under its own name:

```sh
blobfs mv /projects/alpha /projects/alpha2           # path -> path: a rename
blobfs mv id:<alpha-id> /projects/beta/alpha          # id -> path
blobfs mv /projects/beta/alpha id:<delta-id>          # path -> id: into delta
blobfs mv id:<alpha-id> id:<gamma-id>                 # id -> id: into gamma
```

```
mv: /projects/alpha -> /projects/alpha2 (id 01a11799-fba6-73eb-b7ed-bae1eec7775c)
mv: /projects/alpha2 -> /projects/beta/alpha (id 01a11799-fba6-73eb-b7ed-bae1eec7775c)
mv: /projects/beta/alpha -> /projects/delta/alpha (id 01a11799-fba6-73eb-b7ed-bae1eec7775c)
mv: /projects/delta/alpha -> /projects/gamma/alpha (id 01a11799-fba6-73eb-b7ed-bae1eec7775c)
```

### rmdir, and mkdir's one refusal

```sh
blobfs rmdir /projects/gamma/alpha    # rmdir: /projects/gamma/alpha (id …)
blobfs rmdir id:<beta-id>             # rmdir: /projects/beta (id …)
blobfs rmdir /projects                # exit 1: "directory not empty"
blobfs rmdir /                        # exit 2: the root is refused in Validate
```

`mkdir` is the one command that takes a path alone, since the directory it creates has no id
yet. It refuses an id in `Validate`, before anything is built (exit 2):

```sh
blobfs mkdir id:<projects-id>
```

```
blobfs mkdir: a directory is created by path, not by id
Usage: blobfs mkdir [flags] <path>
Run 'blobfs mkdir --help' for details.
```

## Ownership

A unit owns a top-level directory through an owner row that `mkdir --unit` writes with it. The
unit is any UUID:

```sh
unit=0199aaaa-0000-7000-8000-000000000001
blobfs mkdir --unit $unit /team
blobfs mkdir /team/docs
```

```
mkdir: /team (id 01a11799-fdf7-72c8-a1ed-f41ed0414890, unit 0199aaaa-0000-7000-8000-000000000001)
mkdir: /team/docs (id 01a11799-fe24-7f31-b057-7e7a9673d421)
```

At the root, `ls --unit` lists the unit's own top-level directories; below it, the unit must own
the listed directory's top-level ancestor, whether a path or an id names it:

```sh
blobfs ls / --unit $unit                  # one row: team
blobfs ls id:<team-id> --unit $unit       # one row: docs
blobfs ls /projects --unit $unit          # exit 1
```

```
blobfs ls: files: list /projects as unit 0199aaaa-0000-7000-8000-000000000001: /projects: the unit does not own the directory
```

Ownership applies to a top-level directory only, and the owner listing pages by number only.
Both are refused in `Validate` as usage errors (exit 2):

```sh
blobfs mkdir --unit $unit /team/sub
blobfs ls / --unit $unit --after-dirs <next-dirs>
```

```
blobfs mkdir: ownership applies to a top-level directory only; give --unit with a top-level path only
Usage: blobfs mkdir [flags] <path>
Run 'blobfs mkdir --help' for details.
blobfs ls: the owner listing pages by number only; ls / --unit takes no --after-dirs or --after-files
Usage: blobfs ls [flags] <path|id:<uuid>>
Run 'blobfs ls --help' for details.
```

## Objects

Each object command declares the `storage` node, so it opens Postgres and the object store. Make
two local files first:

```sh
tmp=$(mktemp -d)
printf 'hello, blobfs\n' > $tmp/notes.txt
printf 'a,b\n1,2\n' > $tmp/data.csv
```

### put

*Output to be re-captured against SeaweedFS: the etags below are from the Azurite run of the source spike, spike-cli-architecture.*

`put` takes a local file, or stdin for `-`, and a destination path or directory id:

```sh
blobfs put $tmp/notes.txt /projects/notes.txt
blobfs put $tmp/data.csv /projects/data.csv
printf 'from stdin\n' | blobfs put - /projects/stdin.bin
blobfs put $tmp/notes.txt id:<delta-id>
```

```
put: /projects/notes.txt (id 01a11799-fe9e-7909-be7c-d047d039c095, 14 bytes, etag "0x278E323001C6F40")
put: /projects/data.csv (id 01a11799-fec4-7ac8-ae51-734b0f45d3a1, 8 bytes, etag "0x246662AA1485840")
put: /projects/stdin.bin (id 01a11799-fee1-7841-ac84-193fa0a5334f, 11 bytes, etag "0x21D3DB36A3D7760")
put: /projects/delta/notes.txt (id 01a11799-fefe-7ed9-bedf-6afc343d3488, 14 bytes, etag "0x1DEDD42D39F3BF0")
```

The `(id …)` values are `<notes-id>`, `<data-id>`, and `<stdin-id>` below. A name an available
file already holds is refused (exit 1):

```sh
blobfs put $tmp/notes.txt id:<delta-id>
```

```
blobfs put: files: put notes.txt in directory 01a11799-fbec-767f-bfc5-aed09c86877d: data: create file "notes.txt" in 01a11799-fbec-767f-bfc5-aed09c86877d: blobfs: name taken (constraint blobfs_uq_file_directory_name)
```

`stat` on a file shows the content type `put` chose from the extension (`--content-type`
overrides it):

```sh
blobfs stat /projects/notes.txt
```

```
path:         /projects/notes.txt
id:           01a11799-fe9e-7909-be7c-d047d039c095
name:         notes.txt
status:       available
size:         14
content-type: text/plain; charset=utf-8
etag:         "0x278E323001C6F40"
key:          01a11799-fe9e-7909-be7c-d047d039c095/notes.txt
version:      2
created:      2026-10-07T18:22:10Z
updated:      2026-10-07T18:22:10Z
```

### Both cursors

With files in `/projects`, a page of one carries both cursors:

```sh
blobfs ls /projects --size 1 --cursors
```

```
KIND  NAME      SIZE  STATUS     UPDATED              ID
dir   delta     -     -          2026-10-07 18:22:09  01a11799-fbec-767f-bfc5-aed09c86877d
file  data.csv  8     available  2026-10-07 18:22:10  01a11799-fec4-7ac8-ae51-734b0f45d3a1
directories: 1 on page 1 of size 1, total 2
more: yes
next-dirs: LgYem3sidiI6MSwiYmFzZSI6ImRpcmVjdG9yeV9jaGlsZHJlbiIsInRlcm1zIjpbIm5hbWUiXSwiZGVzYyI6ZmFsc2UsImZpbHRlcnMiOiJbe1wiRmllbGRcIjpcInN0YXR1c1wiLFwiT3BcIjpcIm5lXCIsXCJWYWx1ZVwiOlwiZGVsZXRpbmdcIn1dIiwidmFsdWVzIjpbImRlbHRhIl19
files: 1 on page 1 of size 1, total 3
more: yes
next-files: hxluJ3sidiI6MSwiYmFzZSI6ImRpcmVjdG9yeV9maWxlcyIsInRlcm1zIjpbIm5hbWUiXSwiZGVzYyI6ZmFsc2UsImZpbHRlcnMiOiJbe1wiRmllbGRcIjpcInN0YXR1c1wiLFwiT3BcIjpcIm5lXCIsXCJWYWx1ZVwiOlwiZGVsZXRpbmdcIn1dIiwidmFsdWVzIjpbImRhdGEuY3N2Il19
```

```sh
blobfs ls /projects --size 1 --after-dirs <next-dirs> --after-files <next-files>
```

```
KIND  NAME       SIZE  STATUS     UPDATED              ID
dir   gamma      -     -          2026-10-07 18:22:09  01a11799-fbd4-7cd8-98bd-b92fc9a7b66b
file  notes.txt  14    available  2026-10-07 18:22:10  01a11799-fe9e-7909-be7c-d047d039c095
directories: 1 after the cursor, size 1, total not counted
more: no
files: 1 after the cursor, size 1, total not counted
more: yes
```

### cat and cp

*Output to be re-captured against SeaweedFS: the etags in the `cp` lines below are from the Azurite run of the source spike, spike-cli-architecture.*

```sh
blobfs cat /projects/notes.txt     # hello, blobfs
blobfs cat id:<stdin-id>           # from stdin
```

`cp` mixes the forms as `mv` does; a destination directory takes the copy under the source's
name:

```sh
blobfs cp /projects/notes.txt /projects/copy.txt     # path -> path
blobfs cp /projects/data.csv id:<gamma-id>           # path -> id
blobfs cp id:<notes-id> /projects/gamma/notes.txt    # id -> path
```

```
cp: /projects/notes.txt -> /projects/copy.txt (id 01a1179a-000e-795d-b199-8edcd90d5741, 14 bytes, etag "0x1CAA514EEB69720")
cp: /projects/data.csv -> /projects/gamma/data.csv (id 01a1179a-0035-73a2-b080-6a92123e265b, 8 bytes, etag "0x271931C1452FDC0")
cp: /projects/notes.txt -> /projects/gamma/notes.txt (id 01a1179a-0054-7a69-88bf-2934aff8e05b, 14 bytes, etag "0x27B59A9B0839280")
```

`cp` refuses a taken name (exit 1). The source opens only on the body's first read, so the
refusal never reaches the store:

```sh
blobfs cp id:<notes-id> id:<gamma-id>
```

```
blobfs cp: files: copy /projects/notes.txt into /projects/gamma: data: create file "notes.txt" in 01a117b5-b2bc-775d-8d2d-664c66393ce9: blobfs: name taken (constraint blobfs_uq_file_directory_name)
```

The label names the resolved paths, and ends `as <name>` when the copy takes a new name.

### rm and rm --recursive

`rm` prints the resolved path, by path or by id:

```sh
blobfs rm /projects/copy.txt     # rm: /projects/copy.txt (id …)
blobfs rm id:<stdin-id>          # rm: /projects/stdin.bin (id …)
```

`rm --recursive` marks the branch deleting, sweeps it until no work remains, and prints totals.
It takes a path or an id:

```sh
blobfs rm --recursive /projects/gamma
blobfs rm --recursive id:<delta-id>
```

```
rm --recursive: /projects/gamma (2 files, 1 directory)
rm --recursive: /projects/delta (1 file, 1 directory)
```

`rm` refuses the root in either form, in `Validate`, so the refusal is a usage error (exit 2):

```sh
blobfs rm --recursive /
```

```
blobfs rm: blobfs: the root directory; rm removes a file, or with --recursive a directory, below the root
Usage: blobfs rm [flags] <path|id:<uuid>>
Run 'blobfs rm --help' for details.
```

## Bookmarks

A bookmark binds a unit to a file, and at most one of a unit's bookmarks is active. Every
bookmark subcommand requires `--unit` and opens Postgres alone. Put two files under the unit's
`/team/docs`:

```sh
blobfs put $tmp/notes.txt /team/docs/a.txt
blobfs put $tmp/data.csv /team/docs/b.csv     # its id is <b-id>
```

```sh
blobfs bookmark add --unit $unit --active /team/docs/a.txt
blobfs bookmark add --unit $unit --active id:<b-id>          # exit 1: one active per unit
blobfs bookmark add --unit $unit id:<b-id>
```

```
bookmark add: /team/docs/a.txt (file 01a1179a-0117-7d89-9fcf-99f6feea533b, unit 0199aaaa-0000-7000-8000-000000000001, active)
blobfs bookmark add: files: add bookmark of /team/docs/b.csv for unit 0199aaaa-0000-7000-8000-000000000001: the unit has an active bookmark already (constraint uq_bookmark_active)
bookmark add: /team/docs/b.csv (file 01a1179a-0131-7eb6-afc8-bbf1a8e989d7, unit 0199aaaa-0000-7000-8000-000000000001, inactive)
```

`bookmark add /` is a usage error (exit 2): the root is refused in `Validate`.

```sh
blobfs bookmark ls --unit $unit
```

```
PATH              SIZE  STATUS     ACTIVE  UPDATED
/team/docs/a.txt  14    available  active  2026-10-07 18:22:11
/team/docs/b.csv  8     available  -       2026-10-07 18:22:11
bookmarks: 2 on page 1 of size 20, total 2
more: no
```

`rm` refuses a bookmarked file, and `rm --recursive` a branch that holds one, before anything is
deleted (exit 1):

```sh
blobfs rm /team/docs/a.txt
blobfs rm --recursive /team
```

```
blobfs rm: files: remove /team/docs/a.txt: 1 unit bookmarks the file; remove the bookmarks and rerun rm
blobfs rm: files: remove tree /team: 2 bookmarks hold files in the branch; remove the bookmarks and rerun rm --recursive
```

Remove the bookmarks, by path and by id, and the branch goes:

```sh
blobfs bookmark rm --unit $unit /team/docs/a.txt
blobfs bookmark rm --unit $unit id:<b-id>
blobfs rm --recursive /team
```

```
bookmark rm: /team/docs/a.txt (file 01a1179a-0117-7d89-9fcf-99f6feea533b, unit 0199aaaa-0000-7000-8000-000000000001)
bookmark rm: /team/docs/b.csv (file 01a1179a-0131-7eb6-afc8-bbf1a8e989d7, unit 0199aaaa-0000-7000-8000-000000000001)
rm --recursive: /team (2 files, 2 directories)
```

## Bad input builds nothing

Every argument and flag value except a cursor is checked in the leaf's `Validate`, before the
dispatcher builds any node. Each of these is a usage error (exit 2):

```sh
blobfs ls projects
blobfs stat id:not-a-uuid
blobfs ls / --total maybe
blobfs rmdir /
```

```
blobfs ls: blobfs: invalid path: "projects" does not start with /
Usage: blobfs ls [flags] <path|id:<uuid>>
Run 'blobfs ls --help' for details.
blobfs stat: blobfs: invalid id "not-a-uuid": must be a UUID
Usage: blobfs stat [flags] <path|id:<uuid>>
Run 'blobfs stat --help' for details.
blobfs ls: --total "maybe": the mode is exact or none
Usage: blobfs ls [flags] <path|id:<uuid>>
Run 'blobfs ls --help' for details.
blobfs rmdir: blobfs: the root directory; rmdir removes a directory below the root
Usage: blobfs rmdir [flags] <path|id:<uuid>>
Run 'blobfs rmdir --help' for details.
```

Point the database at a closed port and run them again: the output and the exit code are the
same, so nothing was opened. Only valid input reaches the database:

```sh
BLOBFS_DATABASE_PORT=1 blobfs ls projects          # same usage error, exit 2
BLOBFS_DATABASE_PORT=1 blobfs stat id:not-a-uuid   # same usage error, exit 2
BLOBFS_DATABASE_PORT=1 blobfs ls / --total maybe   # same usage error, exit 2
BLOBFS_DATABASE_PORT=1 blobfs rmdir /              # same usage error, exit 2
BLOBFS_DATABASE_PORT=1 blobfs ls /                 # exit 1, naming the database node
```

```
blobfs ls: database: database connection failed: failed to connect to `user=app database=app`:
	127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused
	127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused
```

A malformed cursor is checked only after the graph is built, and exits 1: sqlate exposes no
offline cursor decoder. Against a running database, `ls /projects --after-dirs garbage` ends
`query: cursor is malformed`; against the closed port it fails at the `database` node, as `ls /`
does.

## Per-command dependencies

*Output to be re-captured against SeaweedFS: the store errors and their timing below are from the Azurite run of the source spike, spike-cli-architecture.*

Stop SeaweedFS alone. The compose service is `seaweedfs`:

```sh
docker compose stop seaweedfs
```

Directory and bookmark commands, and the directories scenario, still succeed:

```sh
blobfs mkdir /offline                        # mkdir: /offline (id …)
blobfs ls /                                  # offline, projects
blobfs bookmark ls --unit $unit              # an empty page
blobfs scenario directories | tail -3        # ends with rmdir: /scenario-directories (id …)
```

Object commands fail at start, once, naming the `store` node (exit 1). In the source each waited
about 9 seconds on the Azure SDK's retries first:

```sh
blobfs put $tmp/notes.txt /offline/notes.txt
blobfs cat /projects/notes.txt
blobfs scenario files
```

```
blobfs put: store: storage unavailable: Put "http://127.0.0.1:10010/devstoreaccount1/cliarch?restype=container": dial tcp 127.0.0.1:10010: connect: connection refused
blobfs cat: store: storage unavailable: context deadline exceeded
blobfs scenario files: store: storage unavailable: Put "http://127.0.0.1:10010/devstoreaccount1/cliarch?restype=container": dial tcp 127.0.0.1:10010: connect: connection refused
```

Bring SeaweedFS back, and the same `put` succeeds:

```sh
mise run app:up
blobfs put $tmp/notes.txt /offline/notes.txt   # put: /offline/notes.txt (id …, 14 bytes, etag …)
```

Now take the whole stack down. Help, the scenario listing, `version`, and usage errors declare no
nodes, so they still work:

```sh
mise run app:down
blobfs --help > /dev/null; echo $?   # 2
blobfs scenario | tail -5            # the Scenarios: listing
blobfs version                       # v0.0.0-20261007181626-ac2107ce8ab6
blobfs ls projects                   # the same usage error, exit 2
blobfs mkdir /down                   # exit 1
```

```
blobfs mkdir: database: database connection failed: failed to connect to `user=app database=app`:
	127.0.0.1:5438 (127.0.0.1): dial error: dial tcp 127.0.0.1:5438: connect: connection refused
	127.0.0.1:5438 (127.0.0.1): dial error: dial tcp 127.0.0.1:5438: connect: connection refused
```

```sh
mise run app:up
```

## Scenarios

A scenario narrates each step before doing it, calls the files domain's `Service` or `Storage`
as the command would, and prints the result as the command prints it. Each works in its own
top-level directory, clears it first, and removes it last, so it succeeds twice in a row:

```sh
blobfs scenario directories
```

```
[1/11] Clear /scenario-directories if an earlier run left it behind
  Nothing to clear: /scenario-directories does not exist, so this run starts
  clean.

[2/11] Create /scenario-directories as the scenario unit's: mkdir --unit
  A directory created with a unit is top-level, and its owner row is written in
  the same transaction, so the unit's scope starts here.
    mkdir: /scenario-directories (id 01a1179a-e5cf-7c3a-aefa-0ac7ad7fb952, unit 0199c0de-0000-7000-8000-0000000000de)
…
[9/11] Move echo into alpha, then rename bravo to foxtrot: mv
  A destination that names an existing directory takes the source under its own
  name; one that names a new path renames. Both stay under the one top-level
  directory.
    mv: /scenario-directories/echo -> /scenario-directories/alpha/echo (id 01a1179a-e5d8-7ab3-b655-6fbf760f9bfa)
    mv: /scenario-directories/bravo -> /scenario-directories/foxtrot (id 01a1179a-e5d4-72dc-b808-c81e05d1d32c)
…
```

*Output to be re-captured against SeaweedFS: the etag in step 3 below is from the Azurite run of the source spike, spike-cli-architecture.*

```sh
blobfs scenario files
blobfs scenario files | tail -4   # the second run ends the same way
```

```
[1/9] Clear /scenario-files if an earlier run left it behind
  Nothing to clear: /scenario-files does not exist, so this run starts clean.
…
[3/9] Put hello.txt from memory with no size, as put - streams stdin
  The row is committed pending before any byte is put; the store takes the body
  to its end, and the row is completed with the size and etag the store reports.
    put: /scenario-files/docs/hello.txt (id 01a1179a-e630-7d32-aaed-f41d644b7782, 26 bytes, etag "0x24C634FA8CCCD20")
…
[9/9] Remove the working area and everything under it: rm --recursive
  The branch is marked deleting in one transaction, then swept until no work
  remains: each file's object and row, then each directory once it is empty. A
  rerun starts clean.
    rm --recursive: /scenario-files (2 files, 2 directories)
```

## The checks

`mise run check` is hermetic: for the `app` module it builds, vets, formats, runs `go fix -diff`,
runs the tests with `-race`, and lints, with and without the integration build tag, all in
workspace mode. It skips `go mod tidy -diff` for `app` alone, since tidy cannot resolve the
unpublished `s3` module that `app` finds through `go.work`; the root and `s3` modules run their
whole check, tidy included, with `GOWORK=off`. Its tests drive the command tree over buffers
and run the domain over sqlate's `sqltest` and go-storage's `storagetest.Fake`, with no network:

```sh
mise run check
```

```
ok  	github.com/JaimeStill/spike-s3-storage/app/cli	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/domain/files	1.692s
ok  	github.com/JaimeStill/spike-s3-storage/app/graph	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/internal/app	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/lifecycle	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/migrations	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/output	(cached)
ok  	github.com/JaimeStill/spike-s3-storage/app/scenario	(cached)
0 issues.
0 issues.
```

`mise run app:integration` starts its own compose project, `spike-s3-storage-integration`, on
5439 and 8335, runs the tagged tests, and removes the project with its volumes, pass or fail. The
development stack is untouched. In the source it ran about 45 seconds:

*Output to be re-captured against SeaweedFS: the timings below are from the Azurite run of the source spike, spike-cli-architecture.*

```sh
mise run app:integration
```

```
ok  	github.com/JaimeStill/spike-s3-storage/app/cli	1.043s
ok  	github.com/JaimeStill/spike-s3-storage/app/domain/files	1.720s
ok  	github.com/JaimeStill/spike-s3-storage/app/graph	1.009s
ok  	github.com/JaimeStill/spike-s3-storage/app/integration	38.732s
ok  	github.com/JaimeStill/spike-s3-storage/app/internal/app	2.166s
ok  	github.com/JaimeStill/spike-s3-storage/app/lifecycle	1.072s
ok  	github.com/JaimeStill/spike-s3-storage/app/migrations	1.010s
ok  	github.com/JaimeStill/spike-s3-storage/app/output	1.008s
ok  	github.com/JaimeStill/spike-s3-storage/app/scenario	1.049s
```

What the `integration` package proves:

- **The black-box suite.** It builds `cmd/blobfs` once and runs it as a child process, configured
  only through its environment, arguments, and stdin. `TestScript` runs one ordered script over
  every directory, object, and bookmark command.
- **The pending put.** A `put -` with stdin held open commits a pending row; the test kills it
  with SIGKILL, and a later `put` resumes the row.
- **The interrupted put.** A `put -` with stdin held open receives SIGINT mid-upload; it exits 1
  and reports the cancellation once.
- **The interrupted sweep.** An HTTP relay in front of the store lets one object delete through and
  answers every later one 503. On a branch of three files and an empty directory, `rm
  --recursive` removes exactly one file and the directory and exits 1; the rerun, with the relay
  disarmed, removes the other two.
- **The unreachable store.** With the store endpoint on a closed port, every directory and
  bookmark command succeeds and the object commands fail naming the store.

## A tour of the code, lowest layer first

Each package's `doc.go` describes it and lists its exports. [`STANDARDS.md`](STANDARDS.md)
records the conventions the review settled: the file ontology, one `Commands` call per package,
input checks in `Validate`, and success lines.

- **`graph`**: a typed dependency graph. `Graph.Define` adds a `Node[T]` with its constructor,
  and `Graph.Build(roots...)` constructs only what the roots reach through `Scope.Use`, into a
  `System` of computed layers (`System.Get`, `System.Layers`). `Ref` names a node of any type.
  `Graph.Replace` swaps a constructor for a test, and `Graph.Observe` traces what a Build
  reaches. Open `graph/build.go`; `stages_test.go` expresses go-web-service's stage table.
- **`lifecycle`**: a `Coordinator` over a built `System`. `Exec` starts the layers in order,
  runs a function, and shuts them down in reverse; `Run` serves until its context ends. A value
  takes part by implementing `Starter`, `Stopper`, or both (`Subsystem`). Open
  `lifecycle/coordinator.go`; `stages_test.go` serves go-web-service's stage graph on `Run`.
- **`cli`**: the dispatcher. A `Command` carries `Args`, `Validate`, `Run`, `Footer`, and the
  nodes it declares with `Use`; `Run(ctx, root, args, streams, WithGraph(...))` dispatches.
  `Invocation` embeds `Streams` and reads a declared node with `inv.Get`. Open `cli/run.go`,
  whose `execute` is the dispatch order: Args, Require, Exclusive, Validate, PreRun, Build, Run.
- **`output`**: the two layouts, `Table` and `Record` over `Field`, and `Count`, which pairs a
  count with the noun that agrees with it (`1 file`, `2 files`). Open `output/output.go`.
- **`migrations`** and **`admin/schema`**: `migrations` is the app's own set (`directory_owner`,
  `bookmark`). `schema.NewMigrator` builds sqlate's multi-set migrator over blobfs's set and the
  app's, and `schema.Commands` mounts status, up, down, and reset over the migrator node. Open
  `admin/schema/commands.go`.
- **`domain/files`**: the files domain. `Service` (`service.go`, built by `New`) holds the
  directory and bookmark operations over the database alone, and its `Start` checks every
  statement. `Storage` (`storage.go`, `NewStorage`) holds the object operations over the
  Service and the object store. That split is why directory commands never open the store. Every
  operation takes a `Ref` (`entities.go`): a path or an id. `Commands` (`commands.go`) builds the
  whole surface. The files follow the ontology in `STANDARDS.md`. Open `domain/files/doc.go`,
  then `entities.go`.
- **`scenario`**: the runner (`Scenario`, `Step`, `Reporter`) and the two tours. `Commands`
  mounts `blobfs scenario`, and `WriteListing` writes the footer both help screens end with.
  Open `scenario/directories.go`.
- **`internal/app`**: the composition root. `Nodes` is the single description of the graph;
  `infrastructure.go` defines the configuration nodes, `database`, `store`, and `sql` (the pool in
  sqlate's Postgres dialect, which the migrator and the files Service share). `admin.go`,
  `domain.go`, and `scenario.go` each define their nodes and mount their commands with one
  `root.Add(pkg.Commands(...)...)`. Open `internal/app/app.go`.
- **`cmd/blobfs`**: `main.go` takes the signal context and calls
  `app.New(cli.Streams{...}).Run(ctx, os.Args[1:])`.
- **`internal/apptest`**: fixtures over the App's published graph and nodes. `Builds` records
  what a run builds, `Halt` stops a run before anything starts, and `ScriptDatabase` and
  `FakeStore` replace the database and the store. `internal/app/objects_test.go` shows them in
  use.
- **`integration`**: the black-box suite. Open `integration/integration_test.go` at `TestScript`,
  and `faultRelay` and `crash` near the top.

## Deliberate differences from spike-blobfs

These are spike-cli-architecture's, carried over unchanged by the port.

- No `--dsn`, `--variant`, or `--fail-after`. Configuration comes from `BLOBFS_*` variables, the
  composition root fixes blobfs's Postgres engine, and integration states arise through SIGKILL
  and the fault relay instead.
- No `-r`: the dispatcher has no shorthand flags, so it is `rm --recursive`.
- spike-blobfs has no scenario package. The `scenario` parent follows slab's `demo` and `list`
  convention, consolidated under `blobfs scenario <name>`, and the root's help ends with the
  listing, as `blobfs scenario`'s does.
- Ids are accepted wherever an id can name the target, including `rmdir`, `bookmark add|rm`,
  `rm --recursive`, and `ls --unit`; only `mkdir` is path-only.
- `mv` and `cp` take each argument as a path or an id, so the forms mix.
- `rm --recursive` sweeps the branch to completion in one run, prints totals, and finishes any
  branch an earlier run marked deleting.
- Usage errors exit 2. They include malformed refs, sort terms, filters, `--total` modes, and
  `--unit` values, and the root given to `rmdir`, `rm`, or `bookmark add`. Requested help also
  exits 2. spike-blobfs exited 1 on bad input and 0 on help.
- `put` resumes a pending row; `cp` refuses any taken name and never resumes.
- `bookmark ls` always prints paths.
