// SPDX-License-Identifier: MIT

package setup

import (
	"fmt"
	"time"

	"github.com/phil9922/backup-maker/internal/config"
)

// RemoveFolder stops backing up a folder and drops every dangling reference to
// it from targets and archive jobs.
//
// Files already written to backup targets are left exactly where they are:
// removing a folder is a change of intent, never a deletion of backups.
//
// AND THE FOLDER IS NOT FORGOTTEN, which is the part this used to get wrong.
// Leaving the copies in place while erasing every mention of them made an
// orphan: the destination's manifest is rebuilt from the live folders, so it
// stopped listing them too, and gigabytes of somebody's files were left named
// by nothing at all. A record goes into [[retired]] so the dashboard can offer
// them back — or offer to delete them on purpose. See config.Retired.
func RemoveFolder(id string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, f := range cfg.Folders {
		if f.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no folder with id %q", id)
	}
	// Built BEFORE the folder is unlinked: resolving which destinations hold a
	// copy needs the links that are about to be torn out.
	rec := RetireRecord(cfg, cfg.Folders[idx], time.Now())

	cfg.Folders = append(cfg.Folders[:idx], cfg.Folders[idx+1:]...)
	// THE ID IS DELIBERATELY LEFT IN PLACE on targets and archive jobs, and
	// this is the opposite of what it used to do.
	//
	// An empty Folders list means EVERY folder. So stripping the id out of a
	// job that named exactly one folder emptied the list and silently widened
	// the job from "this folder" to "all of them" — a snapshot schedule
	// created for one directory quietly started sealing up whatever else
	// happened to be configured, on its own timetable, with a password chosen
	// for something else. That is exactly what happened on a real machine:
	// stopping one folder handed its daily snapshot job to another.
	//
	// Left in, the list stays non-empty and therefore stays SCOPED.
	// FoldersForArchive and FoldersForTarget resolve ids against the live
	// folders, so a stopped folder's id simply resolves to nothing: the job
	// covers nothing and does not run, which is what "the folder it backs up
	// is stopped" should mean. Turning the folder back on makes it work again
	// with no bookkeeping at all.
	//
	// The id is only cleaned up once the folder is gone for good — when its
	// retired record is forgotten or its backups deleted. See dropFolderRefs.
	// One record per folder id. Stopping a folder that was already stopped and
	// turned back on replaces the old record rather than accumulating two that
	// describe the same copy.
	kept := cfg.Retired[:0]
	for _, r := range cfg.Retired {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	cfg.Retired = append(kept, rec)
	return cfg.Save()
}

// RetireRecord captures everything needed to put a folder back, or to find and
// delete what it left behind.
//
// THE RESOLUTION HERE IS THE WHOLE POINT. Which destinations hold a copy is
// asked through FoldersForTarget, not read from the literal Target.Folders
// lists, because an empty list means EVERY folder — so a literal reading would
// record no copies at all for exactly the destinations most likely to have one.
// Whether the link was explicit is kept, because putting the folder back must
// not turn an every-folder destination into an explicitly scoped one and
// silently cut off every folder added since.
func RetireRecord(cfg *config.Config, f config.Folder, now time.Time) config.Retired {
	rec := config.Retired{
		ID:               f.ID,
		Path:             f.Path,
		Label:            f.Label,
		ExtraIgnore:      f.ExtraIgnore,
		NoDefaultIgnores: f.NoDefaultIgnores,
		StoppedAt:        now,
		MachineName:      cfg.General.MachineName,
	}
	for _, t := range cfg.Targets {
		for _, held := range cfg.FoldersForTarget(t) {
			if held.ID != f.ID {
				continue
			}
			rec.Copies = append(rec.Copies, config.RetiredCopy{
				Target:   t.Name,
				Type:     t.Type,
				Location: config.TargetLocation(t),
				Explicit: len(t.Folders) > 0,
				// Written down now, while the machine name and label are known
				// to be the ones the files were actually filed under. Both can
				// change later, and a delete must aim at what was written.
				DestPath:     config.DestRoot(cfg.General.MachineName, f.Label),
				VersionsPath: config.VersionRoot(cfg.General.MachineName, f.Label),
			})
			break
		}
	}
	for _, a := range cfg.Archives {
		for _, held := range cfg.FoldersForArchive(a) {
			if held.ID == f.ID {
				rec.Archives = append(rec.Archives, config.RetiredArchive{
					Name:     a.Name,
					Explicit: len(a.Folders) > 0,
				})
				break
			}
		}
	}
	return rec
}

// RemoveTarget stops backing up to a destination. Archive jobs pointed at it
// are removed too, since an archive without a destination cannot run.
//
// As with RemoveFolder, existing backup data on the target is left untouched.
func RemoveTarget(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	idx := -1
	for i, t := range cfg.Targets {
		if t.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no target named %q", name)
	}
	cfg.Targets = append(cfg.Targets[:idx], cfg.Targets[idx+1:]...)

	var keptArchives []config.Archive
	var orphaned []string
	for _, a := range cfg.Archives {
		if a.Target == name {
			orphaned = append(orphaned, a.Name)
			continue
		}
		keptArchives = append(keptArchives, a)
	}
	cfg.Archives = keptArchives

	if err := cfg.Save(); err != nil {
		return err
	}

	// Forget the secrets that belonged to this target only after the config
	// change is durable, so a failed save never orphans a live target from its
	// credentials.
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	delete(state.ShareCredentials, name)
	delete(state.DriveTargetUUIDs, name)
	for _, an := range orphaned {
		delete(state.ArchivePasswords, an)
		delete(state.ArchiveLastRun, an)
	}
	return state.Save()
}

func withoutString(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}
