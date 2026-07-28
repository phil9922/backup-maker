# backup-maker — rules for anyone working on this

## NEVER DELETE FILES FROM A SOURCE FOLDER

This is absolute. There is no exception, no "unless asked", no cleanup case
that overrides it.

backup-maker is **one-way**. A source folder is read from and never written to
or deleted from — not by the mirror engine, not by the snapshot writer, not by
space reclamation, not by any tidy-up path added later. Deleting a *backup* is
a deliberate, confirmed, user-initiated act with its own guards. Deleting a
*source* is never correct at all.

**Why it outranks everything else here.** The product exists so that a machine
failing does not cost somebody their files. A backup tool that can delete the
original is worse than no tool: the user has been told their files are
protected, so they will not have kept a second copy. The failure is
unrecoverable, and it is silent until the moment it matters most.

**What this means in practice**

- Every deletion path must be able to say, in one sentence, why the path it is
  removing is a destination and not a source.
- `RemoveAll` and friends only ever receive a path *inside a destination root*,
  validated first. Recorded paths are untrusted input: `config.toml` is
  hand-editable, so an absolute path or a `..` in a stored field must be
  refused rather than cleaned up and used.
- Any change that could conceivably touch a source needs a test named after the
  guarantee. Copy the pattern from
  `internal/setup/deleteretired_test.go`:
  `TestDeletingRetiredBackupsNeverTouchesTheSourceFolder`.
- The same rule binds anyone operating on a real machine, not just the code:
  before running any `rm`, print the exact paths and confirm none of them is a
  source directory. Prefer moving aside over deleting when unsure.

Existing guards that enforce this, and must not be weakened:
`config.SafeRelPath`, `config.VersionsPathLooksRight`,
`RetiredCopy.SafePaths`, the recognition gate in
`internal/setup/deleteretired.go`, and `Config.DeleteBlockedReason`.

## Other standing rules

- **Removing a folder, a destination or a schedule never deletes backups.**
  Those are changes of intent. The only action in the program that deletes a
  backup on purpose is "Delete backups…" under *No longer protected*, which
  requires the folder's name typed back and is checked in the daemon.
- **An empty `folders` list means EVERY folder.** Never let a scoped list
  become empty by removing entries from it — that silently widens a job from
  one folder to all of them. See `setup.RemoveFolder`.
