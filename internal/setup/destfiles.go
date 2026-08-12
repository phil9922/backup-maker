// SPDX-License-Identifier: MIT

package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/phil9922/backup-maker/internal/archive"
	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/localmirror"
	"github.com/phil9922/backup-maker/internal/statuspage"
)

// The file view: what backup-maker has put on a destination, and which of it
// still belongs to something.
//
// WHY IT EXISTS. Removing a folder, a destination or a schedule deletes
// nothing — that is a deliberate promise — so a destination accumulates copies
// that no longer belong to any task, and nothing in the program could see or
// remove them. 9.9GB of snapshot zips from a schedule that was deleted months
// ago is a directory the owner can only find by plugging the drive into a file
// manager, and can only remove by hand, with no idea which of the neighbouring
// directories a live backup is still writing to.
//
// THE CONTAINMENT RULE, WHICH IS THE WHOLE POINT. This is not a file manager
// for the drive and must never become one. The top level is SYNTHESISED from
// the three places backup-maker writes, never listed from the destination root:
//
//	<machine>/<label>/…                          the live mirror   (config.DestRoot)
//	.backup-maker-versions/<machine>/<label>/…   its history       (config.VersionRoot)
//	backup-maker-archives/<machine>/<job>/*.zip  the snapshots     (archive.PathFor)
//
// and a machine directory counts only when it holds that machine's manifest.
// That filter is load-bearing rather than cosmetic: a bare listing of a
// destination root shows lost+found and everything else the disk happens to
// carry, and this view answers requests for paths — so anything it is willing
// to show is also somewhere a delete could be aimed. locate() is the one door,
// and every request goes through it.
//
// IT LISTS ONE DIRECTORY PER REQUEST AND NEVER WALKS. The mirror engine's
// destIndex walks the whole tree and takes ~26 seconds over SMB for 9,399
// directories; that cost is worth paying once a pass and is not worth paying
// for a click. One ReadDir per click needs no concurrency at all, which is
// just as well — see dirListWorkers, where going past eight had a real Pi
// answering STATUS_INVALID_PARAMETER and refusing listings.

// Kinds of entry. The shape of the layout, so the page can draw a machine
// differently from a folder without parsing paths itself.
const (
	KindMachine  = "machine"
	KindFolder   = "folder"
	KindVersions = "versions"
	KindArchives = "archives"
	KindJob      = "job"
	KindDir      = "dir"
	KindFile     = "file"
)

// Who an entry belongs to, which is the question the whole view exists to
// answer: "can I delete this without breaking a backup?"
const (
	// OwnerInUse: a folder, destination or schedule that is in the
	// configuration and running. Never deletable from here.
	OwnerInUse = "in-use"
	// OwnerStopped: a folder that was stopped and whose copy was kept, so a
	// config.Retired record still names it.
	OwnerStopped = "stopped"
	// OwnerKept: a version whose live counterpart is gone — the last copy of a
	// file deleted from the source. Retention refuses to remove it by age (see
	// localmirror.Prune), and this view is where a person may.
	OwnerKept = "kept"
	// OwnerLeftover: nothing in the configuration refers to it — a deleted
	// schedule's zips, a removed destination's directories, another computer's
	// backups.
	OwnerLeftover = "leftover"
)

// DestEntry is one row of the file view.
type DestEntry struct {
	Name string `json:"name"`
	// Rel is the path to ask for next, relative to the destination root. The
	// browser never constructs one: it sends back what it was given, and the
	// daemon re-derives everything about it anyway.
	Rel      string    `json:"rel"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Kind     string    `json:"kind"`
	Owner    string    `json:"owner"`
	// Why is one sentence saying what this is and who it belongs to.
	Why string `json:"why"`
	// Deletable is decided here, by the same function that refuses the delete
	// itself. A page that offers a button the daemon then refuses is the same
	// bug as a daemon that permits what the page hid.
	Deletable bool `json:"deletable"`
}

// DestListing is ONE directory of a destination.
type DestListing struct {
	Target string `json:"target"`
	// Path is the directory listed; "" is the synthesised top level.
	Path string `json:"path"`
	// Parent is null at the top level and the parent's rel path below it.
	Parent  *string     `json:"parent"`
	Entries []DestEntry `json:"entries"`
}

// DestDeleted reports what a delete actually removed.
type DestDeleted struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Owner  string `json:"owner"`
}

// ListDestFiles lists one directory of what a destination holds. rel is "" for
// the top level and otherwise a path this package handed out earlier.
func ListDestFiles(targetName, rel string, open TargetOpener) (DestListing, error) {
	v, err := openDestView(targetName, open)
	if err != nil {
		return DestListing{}, err
	}
	defer v.close()
	return v.list(rel)
}

// DeleteDestFile removes ONE thing a destination holds.
//
// THE SECOND ACTION IN THIS PROGRAM THAT DELETES A BACKUP ON PURPOSE, and it
// reuses the first one's guard chain rather than inventing a second — see
// DeleteRetiredBackups, whose shape this follows step for step:
//
//  1. The storage must be recognised as ours (localmirror.Recognize, applied in
//     openDestView). A recursive delete landing on a stranger's disk at a
//     familiar mount point is the failure worth most.
//  2. The path must resolve under one of the three roots. Anything else — a
//     "..", an absolute path, a sibling directory on the drive — is refused
//     rather than cleaned up and used. The browser's idea of the layout is
//     never trusted: the roots are recomputed here from the config and the
//     destination.
//  3. config.SafeRelPath with a minimum depth PER ROOT, so a whole store or a
//     whole machine can never be handed to RemoveAll. See loc.minSegments.
//  4. config.VersionsPathLooksRight for anything in the version store.
//  5. Anything a live task owns is refused, in a sentence that names the task.
//  6. confirm must be the entry's own name, checked HERE and not in the page.
//
// The path handed to RemoveAll is relative and the backend is rooted at the
// destination, so no absolute path is ever built and a source directory cannot
// be named by this function at any point.
func DeleteDestFile(targetName, rel, confirm string, open TargetOpener) (DestDeleted, error) {
	v, err := openDestView(targetName, open)
	if err != nil {
		return DestDeleted{}, err
	}
	defer v.close()

	l, err := v.locate(rel)
	if err != nil {
		return DestDeleted{}, err
	}
	// Typed back before anything else is considered, exactly as
	// DeleteRetiredBackups checks the folder's label first. "Are you sure" is
	// answered yes by reflex; typing the name of the thing is not.
	if strings.TrimSpace(confirm) != path.Base(rel) {
		return DestDeleted{}, fmt.Errorf("nothing was deleted: the name typed did not match %q", path.Base(rel))
	}
	fi, err := v.b.Stat(rel)
	if err != nil {
		return DestDeleted{}, fmt.Errorf("there is nothing at %q on %s any more", rel, v.target.Name)
	}
	kind, owner, why := v.describe(l, rel, fi.IsDir(), nil)
	if blocked := v.blockedReason(l, rel, owner, why); blocked != "" {
		return DestDeleted{}, errors.New(blocked)
	}
	if err := v.b.RemoveAll(rel); err != nil {
		return DestDeleted{}, err
	}
	return DestDeleted{
		Target: v.target.Name,
		Path:   rel,
		Name:   path.Base(rel),
		Kind:   kind,
		Owner:  owner,
	}, nil
}

// destView is one open destination and the configuration to read it against.
type destView struct {
	cfg    *config.Config
	state  *config.State
	target config.Target
	b      localmirror.Backend
	// ourMachineDir is this computer's directory name at a destination.
	ourMachineDir string
	// claimed caches, per machine directory, whether this installation owns it,
	// so a listing does not re-read the claim file for every row.
	claimed map[string]bool
}

// openDestView opens a destination for reading or deleting.
//
// THE RECOGNITION GATE COVERS BOTH, and listing is not the lesser case: this
// program refuses to write to storage it does not recognise, and enumerating a
// stranger's disk at a familiar mount point — publishing their directory names
// in a dashboard — is not something to do on the strength of a mount point
// either.
func openDestView(targetName string, open TargetOpener) (*destView, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	var target *config.Target
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == targetName {
			target = &cfg.Targets[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no destination called %q is set up on this computer", targetName)
	}
	// A paired computer holds its copy on its own disk, runs its own
	// backup-maker, and is not a filesystem this machine can list or delete
	// from — the same rule RetiredCopy.Deletable states for the same reason.
	if target.Type == "device" {
		return nil, fmt.Errorf("%q is another computer running backup-maker; what it holds is on its own disk, so it is browsed and deleted over there", targetName)
	}
	state, err := config.LoadState()
	if err != nil {
		return nil, err
	}
	b, err := open(*target)
	if err != nil {
		return nil, fmt.Errorf("%s is not connected", targetName)
	}
	switch localmirror.Recognize(b, state.DriveTargetUUIDs[targetName]) {
	case localmirror.Offline:
		_ = b.Close()
		return nil, fmt.Errorf("%s is not connected", targetName)
	case localmirror.Foreign:
		_ = b.Close()
		return nil, fmt.Errorf("the storage at %s is not the one backup-maker knows, so nothing on it is listed or deleted", targetName)
	}
	return &destView{
		cfg:           cfg,
		state:         state,
		target:        *target,
		b:             b,
		ourMachineDir: config.MachineDir(cfg.General.MachineName),
		claimed:       map[string]bool{},
	}, nil
}

func (v *destView) close() { _ = v.b.Close() }

// The three roots, as the only three answers locate() may give.
const (
	rootMirror   = "mirror"
	rootVersions = "versions"
	rootArchives = "archives"
)

// loc is where a path sits in a destination's layout: which root it is under,
// and its segments. Nothing outside locate() may build one for a path that came
// from a request.
type loc struct {
	root string
	segs []string
}

// machineIndex is which segment names the computer whose backups these are.
func (l loc) machineIndex() int {
	if l.root == rootMirror {
		return 0
	}
	return 1 // both stores are <store>/<machine>/…
}

// machine is the machine segment, or "" for a path that has not reached it.
func (l loc) machine() string {
	if i := l.machineIndex(); len(l.segs) > i {
		return l.segs[i]
	}
	return ""
}

// child extends a located directory by one entry. The slice is copied rather
// than appended in place, so one listing's rows cannot overwrite each other.
func (l loc) child(name string) loc {
	segs := make([]string, len(l.segs)+1)
	copy(segs, l.segs)
	segs[len(l.segs)] = name
	return loc{root: l.root, segs: segs}
}

// minSegments is the shortest path each root will hand to a recursive delete,
// and it is load-bearing in the same way RetiredCopy.SafePaths' 2 and 3 are: it
// guards the SHAPE of the path, so a delete can only ever be aimed at one
// task's data and never at the store or the computer that holds it.
//
//	mirror   ≥2  <machine>/<label>            one folder's copy
//	versions ≥3  <store>/<machine>/<label>    one folder's history
//	archives ≥3  <store>/<machine>/<job>      one schedule's snapshots
//
// So a bare "backup-maker-archives", a lone ".backup-maker-versions" and a
// one-segment machine name are all refused: each would take out far more than
// the person pointed at, and in the machine case would take out backups that
// several folders — or several schedules — are still maintaining.
//
// THE ARCHIVES MINIMUM IS 3, NOT 2. Under that root the machine name is the
// SECOND segment, so 2 would permit "delete every snapshot this computer has
// ever written here" from a single click — the exact counterpart of deleting a
// bare machine directory in the mirror root, which is refused a line above.
// Counting from inside the root rather than from the destination root is the
// only way to read them as different.
func (l loc) minSegments() int {
	switch l.root {
	case rootMirror:
		return 2
	case rootVersions, rootArchives:
		return 3
	}
	// Unreachable: locate() returns an error rather than an unknown root. A
	// number nothing can satisfy is the right way to be wrong about that.
	return len(l.segs) + 1
}

// whatItWouldTake describes, for a path too short to delete, what deleting it
// would actually have removed — so the refusal reads as a reason rather than a
// rule.
func (l loc) whatItWouldTake() string {
	switch {
	case l.root == rootMirror:
		return "everything one computer backs up to this destination"
	case len(l.segs) <= 1 && l.root == rootVersions:
		return "every earlier version of every file kept here"
	case len(l.segs) <= 1:
		return "every snapshot on this destination"
	case l.root == rootVersions:
		return "one computer's whole file history here"
	default:
		return "every snapshot one computer has written here"
	}
}

// locate classifies a requested path and refuses anything that is not inside
// one of the three roots.
//
// THE ONE DOOR. Every listing and every delete comes through here, and the
// answer is derived from the destination and the configuration — never from
// the shape of the string the browser sent. config.SafeRelPath does the
// character work (absolute paths, Windows drive letters, "..", anything
// unclean); the switch below does the containment.
func (v *destView) locate(rel string) (loc, error) {
	outside := fmt.Errorf("%q is not somewhere backup-maker keeps backups on %s", rel, v.target.Name)
	if !config.SafeRelPath(rel, 1) {
		return loc{}, outside
	}
	segs := strings.Split(rel, "/")
	switch segs[0] {
	case config.VersionsDirName:
		return loc{root: rootVersions, segs: segs}, nil
	case archive.DirName:
		return loc{root: rootArchives, segs: segs}, nil
	}
	if !v.isMachineDir(segs[0]) {
		return loc{}, outside
	}
	return loc{root: rootMirror, segs: segs}, nil
}

// isMachineDir reports whether a directory at the destination root holds a
// machine's backups, which it does only when that machine's manifest is inside
// it (setup.ManifestPathFor).
//
// THIS IS THE FILTER THAT KEEPS THE VIEW OFF THE DRIVE. A destination root can
// be a whole disk: lost+found, somebody's photos, the root of an SD card. The
// manifest is the one thing that says "backup-maker filed backups here", and
// without this test the top level would list the disk and every path under it
// would be reachable.
//
// The name is joined verbatim rather than passed through config.MachineDir:
// this string came off the destination, so it is already the directory's real
// name, and sanitising it again would ask about a different directory.
func (v *destView) isMachineDir(name string) bool {
	if name == "" || name == config.VersionsDirName || name == archive.DirName {
		return false
	}
	_, err := v.b.Stat(path.Join(name, ManifestName))
	return err == nil
}

// list returns one directory.
func (v *destView) list(rel string) (DestListing, error) {
	out := DestListing{Target: v.target.Name, Path: rel, Entries: []DestEntry{}}
	if rel != "" {
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		out.Parent = &parent
	}
	if rel == "" {
		entries, err := v.top()
		if err != nil {
			return DestListing{}, err
		}
		out.Entries = entries
		return out, nil
	}

	here, err := v.locate(rel)
	if err != nil {
		return DestListing{}, err
	}
	ents, err := readOneDir(v.b, rel)
	if err != nil {
		return DestListing{}, fmt.Errorf("cannot read %q on %s: %w", rel, v.target.Name, err)
	}
	live := v.liveNamesFor(here, rel)
	for _, d := range ents {
		if hidden(here, d.Name()) {
			continue
		}
		out.Entries = append(out.Entries, v.entry(here.child(d.Name()), path.Join(rel, d.Name()), d, live))
	}
	sortEntries(out.Entries)
	return out, nil
}

// top is the synthesised top level: this destination's machine directories and
// the two stores, and nothing else that happens to be on the storage.
func (v *destView) top() ([]DestEntry, error) {
	ents, err := readOneDir(v.b, "")
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", v.target.Name, err)
	}
	out := []DestEntry{}
	for _, d := range ents {
		// Every file at a destination root is backup-maker's own bookkeeping —
		// the marker, the status index — or somebody else's business. Neither is
		// a backup, so the top level is directories only.
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		var l loc
		switch {
		case name == config.VersionsDirName:
			l = loc{root: rootVersions, segs: []string{name}}
		case name == archive.DirName:
			l = loc{root: rootArchives, segs: []string{name}}
		case v.isMachineDir(name):
			l = loc{root: rootMirror, segs: []string{name}}
		default:
			continue // lost+found, and everything else the storage carries
		}
		out = append(out, v.entry(l, name, d, nil))
	}
	sortEntries(out)
	return out, nil
}

// entry builds one row.
func (v *destView) entry(l loc, rel string, d fs.DirEntry, live map[string]bool) DestEntry {
	e := DestEntry{Name: d.Name(), Rel: rel, Dir: d.IsDir()}
	// A listing carries size and mtime already on both backends, so this asks
	// the destination for nothing extra.
	if info, err := d.Info(); err == nil {
		e.Modified = info.ModTime()
		if !d.IsDir() {
			e.Size = info.Size()
		}
	}
	e.Kind, e.Owner, e.Why = v.describe(l, rel, d.IsDir(), live)
	e.Deletable = v.blockedReason(l, rel, e.Owner, e.Why) == ""
	return e
}

// describe says what one path is, who it belongs to, and why — in one sentence
// the page can show as it stands.
func (v *destView) describe(l loc, rel string, isDir bool, live map[string]bool) (kind, owner, why string) {
	switch l.root {
	case rootMirror:
		if len(l.segs) == 1 {
			owner, why = v.machineOwner(rootMirror, l.segs[0])
			return KindMachine, owner, why
		}
		owner, why = v.folderOwner(l.machine(), path.Join(l.segs[0], l.segs[1]), false)
		return structuralKind(isDir, len(l.segs) == 2, KindFolder), owner, why

	case rootVersions:
		switch len(l.segs) {
		case 1:
			if v.mirroringHere() {
				return KindVersions, OwnerInUse, "Earlier versions of the files backed up here, so a file changed or deleted by mistake can still be got back."
			}
			return KindVersions, OwnerLeftover, "Earlier versions of files that used to be backed up here. Nothing on this computer mirrors to this destination any more."
		case 2:
			owner, why = v.machineOwner(rootVersions, l.segs[1])
			return KindMachine, owner, why
		}
		owner, why = v.folderOwner(l.machine(), path.Join(l.segs[1], l.segs[2]), true)
		// THE LAST COPY OF A DELETED FILE. When a file goes from the source, its
		// destination copy is moved into the store rather than removed, and
		// retention will not take it by age (localmirror.Prune). The version IS
		// the file at that point — so it is not "in use" by the folder, and this
		// view is the only place a person can decide to let it go.
		if !isDir && owner == OwnerInUse && v.versionIsLastCopy(rel, live) {
			return KindFile, OwnerKept,
				"The only copy left of a file that is no longer in the folder — backup-maker keeps it rather than deleting your last copy of something."
		}
		return structuralKind(isDir, len(l.segs) == 3, KindFolder), owner, why

	default: // rootArchives
		switch len(l.segs) {
		case 1:
			owner, why = v.archivesRootOwner()
			return KindArchives, owner, why
		case 2:
			owner, why = v.machineOwner(rootArchives, l.segs[1])
			return KindMachine, owner, why
		}
		owner, why = v.jobOwner(l.machine(), l.segs[2])
		return structuralKind(isDir, len(l.segs) == 3, KindJob), owner, why
	}
}

// structuralKind names an entry by where it sits, unless it is a file — a file
// is a file wherever it turns up.
func structuralKind(isDir, atLevel bool, kind string) string {
	if !isDir {
		return KindFile
	}
	if atLevel {
		return kind
	}
	return KindDir
}

// machineOwner describes a machine directory.
//
// IT HAS TO ASK THE QUESTION ITS ROOT IS ABOUT. This used to ask `mirroringHere`
// whatever root it sat under, so the machine directory inside
// backup-maker-archives — which holds nothing but snapshots — reported itself as
// "what this computer backs up to this destination" purely because some folder
// was being mirrored elsewhere on the same drive. The row above it said "left
// over" and the row inside it said "left over", and this one in between said it
// was live. A person deciding whether to delete 9.9GB was reading a
// contradiction, which is worse than reading nothing.
//
// Under the archives root the question is whether a live SCHEDULE writes here;
// under the mirror and version roots it is whether a folder still mirrors here.
func (v *destView) machineOwner(root string, name string) (owner, why string) {
	if !v.machineIsOurs(name) {
		return OwnerLeftover, fmt.Sprintf("Backups from another computer called %q. They can only be deleted on that computer.", name)
	}
	if root == rootArchives {
		if o, _ := v.archivesRootOwner(); o == OwnerInUse {
			return OwnerInUse, "Where this computer's scheduled snapshots are written."
		}
		return OwnerLeftover, "Snapshots this computer wrote here in the past. No schedule writes to this destination now."
	}
	if v.mirroringHere() {
		return OwnerInUse, "What this computer backs up to this destination."
	}
	return OwnerLeftover, "What this computer used to back up here. Nothing on it is being kept up to date any more."
}

// folderOwner describes anything under one folder's tree, live or historical.
// destRel is the mirror path of the folder itself — <machine>/<label> — which
// is what both the live folders and the stopped records are keyed by.
func (v *destView) folderOwner(machine, destRel string, versions bool) (owner, why string) {
	if !v.machineIsOurs(machine) {
		return OwnerLeftover, fmt.Sprintf("Backups from another computer called %q. They can only be deleted on that computer.", machine)
	}
	for _, f := range v.cfg.Folders {
		if !config.SameDest(config.DestRoot(v.cfg.General.MachineName, f.Label), destRel) {
			continue
		}
		if versions {
			return OwnerInUse, fmt.Sprintf("Earlier versions of files in %q, which is still being backed up here.", f.Label)
		}
		return OwnerInUse, fmt.Sprintf("Backed up here by the folder %q.", f.Label)
	}
	if rec, ok := v.retiredAt(destRel); ok {
		when := ""
		if !rec.StoppedAt.IsZero() {
			when = " on " + rec.StoppedAt.Format("2 January 2006")
		}
		return OwnerStopped, fmt.Sprintf("Kept from %q, which was stopped%s. Nothing is being backed up here now.", rec.Label, when)
	}
	return OwnerLeftover, "Nothing in backup-maker refers to this any more."
}

// retiredAt finds the stopped folder whose copy occupies this directory.
//
// The recorded copy for THIS destination is asked first, because it is what the
// stop actually wrote down. The label is the fallback for a record whose copy
// was never recorded — an older record, or a destination added since — so a
// stopped folder still reads as stopped rather than as an orphan.
func (v *destView) retiredAt(destRel string) (config.Retired, bool) {
	for _, rec := range v.cfg.Retired {
		for _, cp := range rec.Copies {
			if cp.Target == v.target.Name && cp.DestPath != "" && config.SameDest(cp.DestPath, destRel) {
				return rec, true
			}
		}
	}
	for _, rec := range v.cfg.Retired {
		if config.SameDest(config.DestRoot(rec.MachineName, rec.Label), destRel) {
			return rec, true
		}
	}
	return config.Retired{}, false
}

// jobOwner describes a snapshot job's directory and everything in it.
//
// MATCHED ON THE NAME ALONE, not on which destination the schedule currently
// writes to. A job name is unique across the configuration (Validate refuses
// duplicates), and the direction to be wrong in is obvious: refusing to delete
// zips whose schedule was re-pointed elsewhere costs somebody a manual tidy-up,
// while allowing it would delete snapshots a live schedule is still writing.
func (v *destView) jobOwner(machine, job string) (owner, why string) {
	if !v.machineIsOurs(machine) {
		return OwnerLeftover, fmt.Sprintf("Snapshots from another computer called %q. They can only be deleted on that computer.", machine)
	}
	for _, a := range v.cfg.Archives {
		if config.SameDest(archive.PathFor(v.cfg.General.MachineName, a.Name), path.Join(archive.DirName, machine, job)) {
			return OwnerInUse, fmt.Sprintf("Snapshots written by the schedule %q, which is still set up.", a.Name)
		}
	}
	return OwnerLeftover, "Snapshots left by a schedule that no longer exists. Nothing writes here any more."
}

// archivesRootOwner reports whether any schedule still writes to this
// destination at all.
func (v *destView) archivesRootOwner() (owner, why string) {
	for _, a := range v.cfg.Archives {
		if config.SameDest(a.Target, v.target.Name) {
			return OwnerInUse, "Scheduled snapshots — one encrypted zip per run — written here."
		}
	}
	return OwnerLeftover, "Snapshots written here in the past. No schedule on this computer writes to this destination now."
}

// mirroringHere reports whether any folder still mirrors to this destination.
func (v *destView) mirroringHere() bool {
	return len(v.cfg.FoldersForTarget(v.target)) > 0
}

// machineIsOurs reports whether a machine directory at this destination belongs
// to THIS installation.
//
// NAME AND CLAIM, BOTH. A destination can be shared by several computers, and
// two fresh installs of one distro image really are both called "ubuntu" —
// which is why the claim file exists (see localmirror.Claim). A directory whose
// claim belongs to another installation is not ours to delete from even when
// the name matches, and an unclaimed directory under our own name is (backups
// predating claims, and our own directory before the daemon has claimed it).
func (v *destView) machineIsOurs(dir string) bool {
	if dir == "" {
		return false
	}
	key := strings.ToLower(dir)
	if ours, done := v.claimed[key]; done {
		return ours
	}
	ours := config.SameDest(dir, v.ourMachineDir)
	if ours {
		switch st, _ := localmirror.CheckClaim(v.b, dir, v.state.Owns); st {
		case localmirror.ClaimOther, localmirror.ClaimUnreadable:
			ours = false
		}
	}
	v.claimed[key] = ours
	return ours
}

// blockedReason says why this path may not be deleted, or "" when it may.
//
// ONE FUNCTION FOR BOTH ANSWERS: the listing's `deletable` flag and the
// refusal the delete route returns. Two implementations of the same rule
// eventually disagree, and the direction they disagree in is a button that
// promises something the daemon then refuses — or, far worse, one that does
// not appear while the deletion is in fact allowed.
func (v *destView) blockedReason(l loc, rel, owner, why string) string {
	if !config.SafeRelPath(rel, l.minSegments()) {
		return fmt.Sprintf("%q is not one backup: deleting it would remove %s. Open it and delete what you actually mean.",
			rel, l.whatItWouldTake())
	}
	// Belt and braces behind locate(): the path a delete aims at the history
	// must be inside the history, or "removing old versions" would remove the
	// backup itself.
	if l.root == rootVersions && !config.VersionsPathLooksRight(rel) {
		return fmt.Sprintf("%q is not inside the version store, so it will not be deleted as if it were", rel)
	}
	if !v.machineIsOurs(l.machine()) {
		return fmt.Sprintf("%q holds another computer's backups. Delete them on that computer, where the files and the configuration agree.", l.machine())
	}
	if owner == OwnerInUse {
		return fmt.Sprintf("nothing was deleted: %s It is still in use, so stop that backup first if you really want it gone.", why)
	}
	return ""
}

// liveNamesFor is the set of names in the live directory a version directory
// mirrors, or nil when the question does not arise.
//
// ONE EXTRA LISTING RATHER THAN A Stat PER ROW. Telling the last copy of a
// deleted file from a superseded version means asking whether the live file is
// still there; over SMB a stat per version file would be a round trip each, and
// a directory of two hundred versions would take two hundred of them for a
// single click. The live directory answers for all of them in one.
func (v *destView) liveNamesFor(l loc, rel string) map[string]bool {
	if l.root != rootVersions || len(l.segs) < 3 {
		return nil
	}
	liveDir, ok := localmirror.LiveCopyOf(rel)
	if !ok {
		return nil
	}
	ents, err := readOneDir(v.b, liveDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The live directory is gone, so everything here is a last copy.
			return map[string]bool{}
		}
		// Anything else is a question this listing could not get an answer to.
		// nil falls back to asking per file, which fails towards "the live copy
		// is still there" — the answer that offers no delete button.
		return nil
	}
	names := make(map[string]bool, len(ents))
	for _, d := range ents {
		names[d.Name()] = true
	}
	return names
}

// versionIsLastCopy reports whether a version in the store is the only copy of
// its file left on this destination. live, when non-nil, is the listing of the
// directory it mirrors; without it the destination is asked about the one file.
//
// FAILS TOWARDS KEEPING, like localmirror.liveCopyExists: an error that is not
// "not there" answers "a live copy exists", so the row is shown as in use and
// no delete is offered. The wrong answer that way costs a click; the other one
// offers to delete somebody's last copy of a file.
func (v *destView) versionIsLastCopy(rel string, live map[string]bool) bool {
	liveRel, ok := localmirror.LiveCopyOf(rel)
	if !ok {
		return false
	}
	if live != nil {
		return !live[path.Base(liveRel)]
	}
	_, err := v.b.Stat(liveRel)
	return errors.Is(err, fs.ErrNotExist)
}

// hidden reports the entries backup-maker keeps for its own use inside a
// machine's directory: the manifest that makes the directory recognisable, the
// claim that says whose it is, the status page and the copy of the manual.
//
// They are not backups, deleting one would break something the program relies
// on, and none of them is what a person came here to find. Only at the machine
// level, where we write them — a file with one of these names inside somebody's
// backed-up folder is their file, and hiding it would misreport the backup.
func hidden(parent loc, name string) bool {
	if parent.root != rootMirror || len(parent.segs) != 1 {
		return false
	}
	return strings.HasPrefix(name, ".backup-maker-") || name == statuspage.FileName
}

// sortEntries puts directories first and then sorts by name, so a machine's
// folders are not scattered among its files.
func sortEntries(entries []DestEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
}

// readOneDir lists ONE directory and nothing below it.
//
// The Backend interface deliberately has no ReadDir — everything the mirror
// does walks a tree whose shape it already knows — so this takes the DirLister
// both real backends implement and falls back to a walk that is stopped at the
// first level, the same trick localmirror.TopLevelDirs uses. What it must never
// become is a walk: see the file comment, and destindex.go for what listing a
// whole destination costs.
func readOneDir(b localmirror.Backend, dir string) ([]fs.DirEntry, error) {
	if dl, ok := b.(localmirror.DirLister); ok {
		return dl.ReadDir(dir)
	}
	var out []fs.DirEntry
	err := b.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == dir || p == "." {
				return walkErr
			}
			return nil
		}
		if p == dir || p == "." {
			return nil
		}
		out = append(out, d)
		if d.IsDir() {
			return fs.SkipDir // exactly one level
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
