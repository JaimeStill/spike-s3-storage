# Standards

The judgement calls the check can't enforce for the `app` module, one line each, for the
standards-reviewer. They came with the app from spike-cli-architecture, and cover `app/` alone,
not the root or `s3` modules. What `mise run check` enforces is never restated here.

The architecture pages that apply, in the architecture repository:

- `principles/composition-root.md`
- `principles/tool-beside-library.md`
- `principles/minimal-footprint.md`
- `standards/go-elemental/principles/topology-and-naming.md`
- `standards/go-elemental/principles/tests-and-docs.md`
- `standards/go-elemental/principles/domain-files.md`, the domain-file ontology

## Types and files

- A type or layer is introduced only when it is needed: no pass-through wrapper, no handle type
  over a graph node, and no exported symbol that nothing outside its package reads.
- A file holds one primary type or subject and is named for it, as `cli/invocation.go` holds
  `Invocation`.
- A domain package's files follow the domain-file ontology: `service.go` the exported `Service`,
  built by `New`; `database.go` the unexported `store` and its queries; `storage.go` the blob
  protocol and, in a CLI, the second API type its dependency profile splits off (`Storage`,
  `NewStorage`); `entities.go` the shapes; `errors.go` the errors; `commands.go` the commands,
  their flag structs, and the CLI-syntax parsers; `output.go` the writers and record layouts.

## Commands

- A package mounts through one `Commands` call, which returns `[]*cli.Command` whether it builds
  one command or many, so every mount is `root.Add(pkg.Commands(...)...)`; each command is a
  plain function of the nodes it declares with `Use`, reading them with `inv.Get`.
- `Args` only counts positionals; every argument and flag value, the domain's form rules
  included, is checked in `Validate`, so bad input builds nothing.
- An operation that names an entry takes the domain's reference type (`files.Ref`) and accepts
  a path or an id wherever an id can name the target, each argument of a pair on its own (mv and
  cp mix them); only mkdir is path-only, by nature (the new directory has no id), and it refuses
  an id with a typed form error before any I/O.
- A domain error is worded in the domain's terms; the command adds flag or command wording where
  it reports the error.
- A command that changes state is never silent: it prints one success line with `fmt.Fprintf`.
- A success line or a record names an entry by its resolved path, whichever form the argument
  took.

## Composition

- A value takes part in a lifecycle only through the single-method interfaces it implements,
  `lifecycle.Starter` and `lifecycle.Stopper`; nothing registers hooks.
- A wiring mistake panics at construction or dispatch with its package's prefix; a failure that
  input or the environment can cause returns an error.
- Configuration is a graph node, finalized in its constructor, so the environment is read only
  for a built System and never for help, a usage error, or a command that declares nothing.
