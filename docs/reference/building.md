# Building from source, and how a release is cut

For contributors and anyone who would rather compile it themselves.

## Architecture

One long-lived process (`backup-maker daemon`) hosts three subsystems:

1. **Syncthing supervisor** (`internal/syncthing`) — for paired-machine
   targets. Started lazily: only when a device target or receive mode is
   configured does it download a pinned, SHA256-verified Syncthing v2.1.2
   (once), run it as a child process with an isolated home dir (never touching
   a user's own Syncthing), and drive it via its loopback REST API.
   Drive/share-only setups never start or fetch it. Source folders
   are `sendonly`, receiving machines enforce `receiveonly` + staggered
   versioning. Local-only, enforced in code: global discovery, relays, NAT
   traversal, and
   telemetry disabled.
2. **Mirror engine** (`internal/localmirror`) — for drive and network-drive
   targets. One goroutine per folder × target: fsnotify watching with
   debounce, hourly full-reconcile backstop, atomic writes (temp + fsync +
   rename), delete-propagation-as-versioning into `.backup-maker-versions/`
   with staggered pruning, UUID marker files for foreign-storage refusal. The
   destination is abstracted behind a `Backend` interface with two
   implementations: `localFS` (os calls) and `smbfs` (built-in SMB 2/3 client,
   `github.com/cloudsoda/go-smb2`). SMB targets add read-back SHA256
   verification and mtime self-calibration (servers that ignore timestamps
   get size+recency comparison).
3. **Web UI / API server** (`internal/webui`) — stdlib `net/http` bound to
   `127.0.0.1` only, vanilla JS + `go:embed` (no build chain). Token-auth
   (bearer for CLI, cookie for browser; the cookie is `HttpOnly` +
   `SameSite=Strict`, and a `Host`-header check rejects anything but
   loopback). The CLI's mutating commands edit `config.toml`; the daemon
   watches the file and reapplies within seconds.

Other packages: `internal/config` (TOML config + private `state.json`),
`internal/setup` (folder/target/archive creation shared by the CLI and the
dashboard, including the all-or-nothing multi-destination commit),
`internal/browse` (directory listing for the folder picker — directories only,
never file names — plus local drive detection), `internal/machines` (presents
destinations as a list of computers rather than a mix of paths, share URLs and
device IDs), `internal/discover` (on-demand LAN scan: TCP 445 sweep +
hand-rolled NetBIOS name query + guest share listing), `internal/wol`
(Wake-on-LAN magic packets, rate-limited per target), `internal/pairing`
(device approval,
auto-accept of offered folders as receive-only), `internal/status` (unified
health model for CLI table + dashboard), `internal/autostart` (systemd user
unit / launchd agent / Windows Run key), `internal/setup` (target-creation
flows shared by CLI and dashboard), `cmd/` (cobra CLI).

## Building from source

Requires the Go version in `go.mod` (currently 1.26.5) or newer. `go build`
produces the single binary; cross-compile with `GOOS=windows go build`, etc.
Run the SMB integration tests against any SMB server:
`BM_SMB_TEST_URL=//host:port/share BM_SMB_TEST_USER=u BM_SMB_TEST_PASS=p go test ./internal/smbfs`.

A binary you build yourself reports `dev` from `backup-maker version`, along
with the commit it came from and whether your tree had uncommitted changes.

Building for a Raspberry Pi has its own architecture caveats — see
[Building for the Pi](../setup/raspberry-pi.md#building-for-the-pi).

### Cutting a release

Releases are built by [GoReleaser](https://goreleaser.com) from
`.goreleaser.yaml`, so they're reproducible rather than hand-built: `-trimpath`
keeps local filesystem paths out of the binaries, and timestamps come from the
commit, so the same tag always produces identical output. Pushing a version tag
is the whole process — `.github/workflows/release.yml` runs the tests, builds
every target, and publishes the GitHub release with checksums:

```sh
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

To see what a release would produce without publishing anything:

```sh
goreleaser release --snapshot --clean   # writes to dist/
```
