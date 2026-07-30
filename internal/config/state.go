// SPDX-License-Identifier: MIT

package config

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"
)

// State is machine-owned runtime data, distinct from the user-editable
// config.toml. The CLI reads it to find and authenticate to the daemon.
type State struct {
	// IPCToken authenticates CLI/dashboard requests to the daemon API.
	IPCToken string `json:"ipc_token"`
	// InstallID identifies this INSTALLATION, minted once and never rotated. It
	// answers the one question no other identifier can: two computers can share
	// a machine_name (it defaults to the hostname, and two fresh installs of one
	// distro image really are both "ubuntu"), and they would then share a
	// <machine>/ directory on a destination and version each other's files away.
	// The claim files that prevent it are keyed on this.
	//
	// NOT A SECRET — it is written to every destination — which is exactly why
	// it is not the IPC token or the syncthing API key. It is not the syncthing
	// DeviceID either: that exists only while the sync engine is running, and a
	// machine backing up to a drive alone never starts it.
	//
	// InheritedInstallIDs are ids this machine took over by adopting a
	// destination and continuing as the machine that wrote it. A claim held by
	// one of them reads as ours, which is what lets a rebuilt computer pick up
	// destinations that were not plugged in at adoption time.
	InstallID           string   `json:"install_id,omitempty"`
	InheritedInstallIDs []string `json:"inherited_install_ids,omitempty"`
	// DashboardPort is the port the daemon actually bound (normally the
	// configured one).
	DashboardPort int `json:"dashboard_port,omitempty"`
	// SyncthingAPIKey and SyncthingGUIPort locate our private syncthing child.
	SyncthingAPIKey  string `json:"syncthing_api_key,omitempty"`
	SyncthingGUIPort int    `json:"syncthing_gui_port,omitempty"`
	// DriveTargetUUIDs maps drive/share target name -> UUID written into the
	// target's marker file, so different storage appearing at the same
	// location is refused.
	DriveTargetUUIDs map[string]string `json:"drive_target_uuids,omitempty"`
	// ShareCredentials maps share-target name -> SMB password. state.json is
	// 0600 and machine-owned: plaintext-but-private, the same trust level as
	// the IPC token and syncthing API key above.
	//
	// Unless SecretsInKeyring is set, in which case the FILE holds
	// KeyringSentinel here and this map is filled from the OS keyring on load.
	ShareCredentials map[string]string `json:"share_credentials,omitempty"`
	// SecretsInKeyring says the share passwords above and the archive passwords
	// below are kept in the OS keyring, with a KeyringSentinel standing in for
	// each value in this file. OPT-IN, off by default, never required — see
	// internal/config/keyring.go for the whole of why, and for why the keys stay
	// in the file even when the values do not.
	//
	// IN state.json DESPITE NOT BEING A CREDENTIAL, which is the opposite of the
	// reason every other field here is. It is a fact about THIS machine's
	// keyring: whether one exists, and whether this install was told to use it.
	// config.toml is the file that is safe to paste into an issue report and to
	// copy to another machine, and this flag copied to a headless Pi would
	// describe storage that does not exist there — a config that says "the
	// passwords are in the keyring" on a machine with no keyring at all.
	SecretsInKeyring bool `json:"secrets_in_keyring,omitempty"`
	// MissingFromKeyring names the keyring accounts (ShareKeyringAccount,
	// ArchiveKeyringAccount) whose secret the keyring would not give up on the
	// last load — a locked keyring, or a daemon started at boot with no keyring
	// session. Derived on every load, so it is never serialized.
	//
	// IT EXISTS SO THAT A MISS IS LOUD. Two things depend on it. Save puts the
	// sentinel back for these keys, because the file is the only record that the
	// secret exists at all and dropping the key would strand the value in the
	// keyring with nothing pointing at it. And RenameTarget refuses outright
	// while a destination is listed here, because a rename in that state moves a
	// name and leaves the password behind.
	MissingFromKeyring map[string]bool `json:"-"`
	// WebhookURL is where alerts are POSTed when the webhook sink is on.
	//
	// HERE RATHER THAN IN config.toml because it is usually a credential: a
	// Slack or Discord endpoint URL grants anyone holding it the right to post,
	// and an ntfy topic URL often carries a token. Same treatment as the share
	// and archive passwords two fields up.
	WebhookURL string `json:"webhook_url,omitempty"`
	// NtfyTopicURL is the ntfy topic alerts are published to, and NtfyToken the
	// access token for it when the topic is protected.
	//
	// BOTH HERE FOR THE SAME REASON AS WebhookURL, and the topic more urgently
	// than it looks: on ntfy.sh the topic name IS the access control, so a
	// config.toml carrying it could not be pasted into an issue report without
	// handing over every alert this machine sends.
	NtfyTopicURL string `json:"ntfy_topic_url,omitempty"`
	NtfyToken    string `json:"ntfy_token,omitempty"`
	// UpdateLastCheck is when github.com was last asked about releases, so a
	// daemon that restarts often does not ask on every start. Persisted rather
	// than kept in memory precisely because a restart loop is the case that
	// would otherwise hammer a public API.
	UpdateLastCheck time.Time `json:"update_last_check,omitzero"`
	// UpdateLatest is the newest release seen, and UpdateAnnounced the one
	// already mentioned to the user — kept apart so an update is announced ONCE
	// rather than every day until it is installed. A nag is how somebody learns
	// to ignore the one alert that matters.
	UpdateLatest    string `json:"update_latest,omitempty"`
	UpdateAnnounced string `json:"update_announced,omitempty"`
	// LANDevices remembers the browsers that have asked to read the read-only
	// network view, keyed by the token that view issued them.
	//
	// IN state.json, NOT config.toml, because the keys are credentials — the
	// same reason share passwords and the IPC token live here. The file is
	// 0600 and never leaves the machine.
	LANDevices map[string]*LANDevice `json:"lan_devices,omitempty"`
	// AdvisorSeen records only that the setup-advisor quiz was offered once,
	// so it doesn't re-prompt. No quiz answers are ever stored.
	AdvisorSeen bool `json:"advisor_seen,omitempty"`
	// SetupComplete records that first-run setup was finished or deliberately
	// skipped, so the dashboard stops opening the wizard. It is only a
	// "don't ask again" marker: the wizard also stands down as soon as a
	// folder and a target exist, so a CLI-configured machine never sees it.
	SetupComplete bool `json:"setup_complete,omitempty"`
	// ArchivePasswords maps archive name -> the REQUIRED zip password (same
	// privacy level as ShareCredentials: 0600, machine-owned, never in
	// config.toml, and moved into the OS keyring by the same SecretsInKeyring
	// flag). Losing this password means losing access to the archives; the
	// wizard says so out loud.
	ArchivePasswords map[string]string `json:"archive_passwords,omitempty"`
	// ArchiveLastRun tracks when each archive job last completed, so
	// schedules survive daemon restarts and overdue jobs catch up.
	ArchiveLastRun map[string]time.Time `json:"archive_last_run,omitempty"`
	// MirrorLastSync tracks when each folder last completed a sync to each
	// drive/share destination — folder id -> target name -> time — so those
	// clocks survive a daemon restart too. Without it a reboot resets every
	// destination to "never synced", and a drive that has been unplugged for
	// months reads as merely offline rather than stale. Paired machines need no
	// entry here: their last-seen comes from the sync engine's own on-disk
	// statistics, which already survive restarts.
	//
	// Nested rather than one joined key, because target names are free-form text
	// from config.toml and a hand-edited folder id can be anything too: there is
	// no delimiter both are guaranteed not to contain, and a collision here
	// would hand one destination another's clock.
	//
	// The daemon batches writes to this alongside the counters below (see
	// internal/daemon/tally.go): a busy folder syncs every few seconds, and
	// state.json is not a log.
	MirrorLastSync map[string]map[string]time.Time `json:"mirror_last_sync,omitempty"`
	// MirrorScanState is what each folder × destination pair learned on its last
	// COMPLETED pass — folder id -> target name -> ScanMark. Nested for exactly
	// the reason MirrorLastSync is.
	MirrorScanState map[string]map[string]ScanMark `json:"mirror_scan_state,omitempty"`
	// BytesCopiedTotal and FilesCopiedTotal are the lifetime odometer: every
	// byte and file backup-maker has itself written to a destination since
	// CountingSince, re-copies of a changed file included. It lives here rather
	// than in config.toml because it is machine-owned bookkeeping, and here
	// rather than on the destinations because it is a fact about this
	// installation's work, not about any one drive.
	//
	// It counts ONLY what this program copies: the mirror engine's drive and
	// share targets, and the snapshot writer. Data sent to a paired machine
	// travels through the sync engine and is not counted — see status.Totals,
	// which carries the target mix so nothing ever renders this as a bare
	// number without saying what it covers.
	//
	// The daemon batches writes to these (see internal/daemon/tally.go): losing
	// a few seconds of counter to a crash is fine, rewriting state.json once
	// per copied file is not.
	BytesCopiedTotal uint64    `json:"bytes_copied_total,omitempty"`
	FilesCopiedTotal uint64    `json:"files_copied_total,omitempty"`
	CountingSince    time.Time `json:"counting_since,omitzero"`
	// RecentAlerts is what this machine has told you, newest last. See
	// AlertRecord.
	RecentAlerts []AlertRecord `json:"recent_alerts,omitempty"`
}

// MaxAlertHistory is how many alerts are kept. Alerts fire on transitions, not
// on a timer, so fifty of them is months of an ordinary machine and still a few
// kilobytes — and the ones that matter are read within a day or two of being
// raised.
const MaxAlertHistory = 50

// AlertRecord is one alert this machine raised, kept so there is a record of
// what happened and not only of what is happening.
//
// WHY IT IS PERSISTED, unlike the delivery outcome shown beside it on the
// dashboard, and the contrast is the whole design. status.DeliveryInfo answers
// "is the webhook working right now", which a restart must not answer out of
// last week's memory. This answers "what has this machine told me, and did it
// get out" — which is history. A desktop notification is gone the moment it is
// dismissed, a phone notification the moment it is swiped, and on a headless
// machine neither was ever shown; without this there is no record anywhere that
// backups stopped at 3am and started again at 6.
type AlertRecord struct {
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	Body  string    `json:"body,omitempty"`
	// Urgent marks the alerts that mean something is wrong, as opposed to an
	// all-clear or a piece of news. It is stored rather than re-derived from the
	// title, because the title is prose and prose gets rewritten.
	Urgent bool `json:"urgent,omitempty"`
	// Delivered names the methods that took this alert, Failed the ones that
	// were tried and could not.
	//
	// BOTH EMPTY MEANS NOTHING WAS EVEN TRIED — no desktop, no webhook, no ntfy
	// — which is the default state of a headless machine and exactly the case
	// worth being able to see. An alert raised and delivered nowhere is the
	// silence this whole feature exists to break, and it is invisible
	// everywhere else.
	Delivered []string `json:"delivered,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

// RecordAlert adds one alert to the history, keeping it in time order and
// discarding the oldest past MaxAlertHistory.
//
// Inserted by time rather than appended, because delivery happens on its own
// goroutine: two alerts raised a moment apart can finish out of order, and a
// history that is not in order is one somebody has to read twice to trust.
func (s *State) RecordAlert(r AlertRecord) {
	i := len(s.RecentAlerts)
	for i > 0 && s.RecentAlerts[i-1].At.After(r.At) {
		i--
	}
	s.RecentAlerts = append(s.RecentAlerts, AlertRecord{})
	copy(s.RecentAlerts[i+1:], s.RecentAlerts[i:])
	s.RecentAlerts[i] = r
	if n := len(s.RecentAlerts); n > MaxAlertHistory {
		s.RecentAlerts = append([]AlertRecord(nil), s.RecentAlerts[n-MaxAlertHistory:]...)
	}
}

// ScanMark is what one folder × destination pair learned on its last COMPLETED
// pass, so a restart does not begin from ignorance.
//
// ONLY EVER WRITTEN FOR A PASS THAT FINISHED. A pass that was interrupted
// learned nothing it is entitled to claim: promoting a half-pass's clock would
// tell the next one that files it never reached had already been checked. The
// mark can therefore only ever cause a redundant recopy, never a skip — which
// is the property that makes it safe to lose in a crash.
type ScanMark struct {
	// PassStart is when the last completed pass BEGAN — not when it ended.
	// It is the engine's "changed since the last scan" clock, and the end time
	// would be blind to everything edited while the pass was running.
	PassStart time.Time `json:"pass_start,omitzero"`
	// MtimeTrusted caches the probe that asks whether this destination
	// preserves file timestamps. nil means never calibrated.
	//
	// THIS IS THE ONE THAT COSTS REAL BYTES. On a destination that does NOT
	// preserve them — FAT-formatted USB on a router, NAS firmware that ignores
	// SetInfo — the engine falls back to comparing size against the last scan
	// time, and that time lives in memory alone. Every restart reset it to zero,
	// which reads as "everything changed", so every same-size file in the folder
	// was recopied. On a machine restarted 54 times in a day, onto the slowest
	// class of destination there is.
	MtimeTrusted *bool `json:"mtime_trusted,omitempty"`
	// DestFiles is how many files that pass found on the destination — the only
	// honest denominator the next pass's listing phase can show.
	DestFiles int64 `json:"dest_files,omitempty"`
	// TargetUUID ties this mark to the storage it was learned from. Different
	// storage under the same target name has learned nothing and must not
	// inherit another drive's clock — the same reasoning that makes
	// DriveTargetUUIDs worth recording at all.
	TargetUUID string `json:"target_uuid,omitempty"`
}

func LoadState() (*State, error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	s.hydrateFromKeyring()
	return &s, nil
}

// hydrateFromKeyring replaces each sentinel in the two secret maps with the real
// value out of the OS keyring.
//
// HERE, IN LoadState, AND NOWHERE ELSE — this and forFile below are the entire
// seam. Between them, an in-memory State always holds real secret values and
// every one of the dozens of readers (the mirror engines, the snapshot writer,
// the wizard, the adopt path, the status page) goes on reading a plain map and
// does not know this feature exists. A per-call-site accessor would be the same
// feature with one more place to forget it, and the place somebody forgot would
// be a destination that stops backing up.
//
// A SECRET IT CANNOT GET IS DELETED FROM THE MAP, NEVER LEFT AS THE SENTINEL.
// The sentinel is an ordinary string, and a caller handed it will use it: the SMB
// client would try to log in with KeyringSentinel as the password, which the
// server rejects and which surfaces to the user as a wrong password on a machine
// where the password is perfectly correct. The snapshot writer is worse — it
// would encrypt a zip with the sentinel, and that archive's real password is then
// not the one in anybody's keyring and not written down anywhere. A missing entry
// makes a caller MISS, which every caller already handles, and which the daemon
// reports at startup by name.
//
// Never fails. A keyring that will not open is a reason to say so, not a reason
// for the CLI and the daemon to refuse to read their own state file.
func (s *State) hydrateFromKeyring() {
	if !s.SecretsInKeyring {
		return
	}
	for _, kind := range []struct {
		secrets map[string]string
		prefix  string
	}{
		{s.ShareCredentials, shareAccountPrefix},
		{s.ArchivePasswords, archiveAccountPrefix},
	} {
		for key, value := range kind.secrets {
			if value != KeyringSentinel {
				// A real value left in the file by a keyring write that failed
				// (see storedSecrets). It is the truth; leave it alone, and the
				// next save will try the keyring for it again.
				continue
			}
			account := kind.prefix + key
			secret, err := KeyringFetch(account)
			if err != nil {
				delete(kind.secrets, key)
				if s.MissingFromKeyring == nil {
					s.MissingFromKeyring = map[string]bool{}
				}
				s.MissingFromKeyring[account] = true
				continue
			}
			kind.secrets[key] = secret
		}
	}
}

// KeepReachableSecrets fills in, from a State this process already holds, the
// secrets a fresh load could not get out of the OS keyring.
//
// FOR THE DAEMON'S CONFIG RELOAD, and it is the same judgement as the
// never-rotate rule on the IPC token beside its call site: for a secret, memory
// is the better source. The daemon re-reads state.json every time config.toml
// changes, and a keyring can stop answering while it runs — a relock, a restarted
// session bus, a keyring daemon that died. Without this, saving an unrelated
// setting would strip the share passwords out of a daemon that had them in memory
// and had been using them successfully all day, and every share destination would
// stop. That is precisely the failure keyring storage is not allowed to introduce.
//
// A KEY THE FILE NO LONGER LISTS IS NOT CARRIED OVER, because that is a removal
// and removals must stick: only accounts the file still records — the ones in
// MissingFromKeyring — are considered at all.
//
// The miss is then no longer a miss, so the account is cleared: this State does
// hold the real value. The cost is that a save happening while the keyring is
// still shut writes that value to state.json in plaintext, which is the ordinary
// fallback (0600, machine-owned, and reported through KeyringWriteFallback), and
// which the next save with a working keyring undoes.
func (s *State) KeepReachableSecrets(prev *State) {
	if prev == nil || len(s.MissingFromKeyring) == 0 {
		return
	}
	for account := range s.MissingFromKeyring {
		if key, ok := strings.CutPrefix(account, shareAccountPrefix); ok {
			if secret, held := prev.ShareCredentials[key]; held {
				if s.ShareCredentials == nil {
					s.ShareCredentials = map[string]string{}
				}
				s.ShareCredentials[key] = secret
				delete(s.MissingFromKeyring, account)
			}
			continue
		}
		if key, ok := strings.CutPrefix(account, archiveAccountPrefix); ok {
			if secret, held := prev.ArchivePasswords[key]; held {
				if s.ArchivePasswords == nil {
					s.ArchivePasswords = map[string]string{}
				}
				s.ArchivePasswords[key] = secret
				delete(s.MissingFromKeyring, account)
			}
		}
	}
}

// SecretMissing reports whether one keyring account's secret was unreachable on
// the last load. Callers that are about to move or re-key a secret ask this
// first: moving a name while the value is out of reach is how the value is lost.
func (s *State) SecretMissing(account string) bool {
	return s.MissingFromKeyring[account]
}

// KeyringMisses lists the unreachable accounts in a stable order, for a message
// somebody can act on. Sorted because it is printed and logged, and a set
// iterated in map order reads like a different problem every time.
func (s *State) KeyringMisses() []string {
	if len(s.MissingFromKeyring) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.MissingFromKeyring))
	for account := range s.MissingFromKeyring {
		out = append(out, account)
	}
	sort.Strings(out)
	return out
}

// Owns reports whether a claim recorded under id belongs to this installation,
// counting the ids inherited by adoption as well as our own.
//
// A method on State rather than a comparison at each call site because "is this
// ours" is asked on the write path of every destination, and the inherited case
// is precisely the one a hand-rolled `id == s.InstallID` would miss — turning a
// correctly adopted machine into a refusal to back up.
func (s *State) Owns(id string) bool {
	if id == "" || s == nil {
		return false
	}
	if id == s.InstallID {
		return true
	}
	for _, inherited := range s.InheritedInstallIDs {
		if id == inherited {
			return true
		}
	}
	return false
}

// EnsureInstallID returns this installation's id, minting and saving one on
// first use.
//
// Callable with no daemon running, because the CLI's add-target commands need
// an id before anything else exists: they are the first thing that writes a
// claim to a destination.
func EnsureInstallID() (string, error) {
	s, err := LoadState()
	if err != nil {
		return "", err
	}
	if s.InstallID != "" {
		return s.InstallID, nil
	}
	s.InstallID = NewToken()[:16]
	if err := s.Save(); err != nil {
		return "", err
	}
	return s.InstallID, nil
}

func (s *State) Save() error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.forFile(), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// forFile is what actually gets written: s itself in the ordinary case, and a
// redacted clone when the secrets live in the OS keyring.
//
// A CLONE, NEVER s ITSELF, and this is the sharpest edge in the feature. The
// daemon holds this exact State in memory for its whole life and hands the share
// passwords to the mirror engines out of it; Save is called from the tally's
// flush every few seconds. Redacting in place would therefore replace live
// passwords with a sentinel a few seconds after startup, and every share
// destination on the machine would begin failing to authenticate — on a timer,
// with nothing having changed, on the one code path nobody is watching.
func (s *State) forFile() *State {
	if !s.SecretsInKeyring {
		return s
	}
	// Shallow: everything else in the struct is written as it stands, and this
	// clone lives only long enough to be marshalled.
	clone := *s
	clone.ShareCredentials = s.storedSecrets(s.ShareCredentials, shareAccountPrefix)
	clone.ArchivePasswords = s.storedSecrets(s.ArchivePasswords, archiveAccountPrefix)
	return &clone
}

// storedSecrets is one secret map as the file should hold it: the values put into
// the keyring and replaced with the sentinel, the keys left exactly where they
// were.
//
// A FAILED KEYRING WRITE FALLS BACK TO PLAINTEXT, deliberately, and this is the
// rule the whole design turns on. The user asked for their password to be kept
// somewhere better; they did not ask for it to be thrown away. If the keyring
// will not take it, the only two options are the file — where it lived safely for
// every version before this one, 0600 and machine-owned — or nowhere, which means
// a destination that cannot be reached until somebody remembers a password they
// were told they no longer had to. So it goes in the file, and
// KeyringWriteFallback says so out loud, because a fallback nobody is told about
// is indistinguishable from a lie.
//
// Never deletes from the keyring, and never drops a key: see MissingFromKeyring.
func (s *State) storedSecrets(secrets map[string]string, prefix string) map[string]string {
	stored := make(map[string]string, len(secrets))
	for key, secret := range secrets {
		account := prefix + key
		if err := KeyringPut(account, secret); err != nil {
			stored[key] = secret
			reportKeyringFallback(account, err)
			continue
		}
		stored[key] = KeyringSentinel
	}
	// The entries this load could not read back. Their keys were removed from the
	// in-memory map (so that nothing uses a sentinel as a password) and must go
	// back into the FILE, because the file is the only record that the secret
	// exists: written without them, it would leave a password in the keyring that
	// nothing on this machine can ever name again.
	for account := range s.MissingFromKeyring {
		key, ok := strings.CutPrefix(account, prefix)
		if !ok || key == "" {
			continue // the other kind of secret; its own call handles it
		}
		if _, present := secrets[key]; present {
			// Set again since the failed load — by `set-password`, by the wizard,
			// or read back once the keyring opened. Whatever is in the map now is
			// newer than this record of a miss, and overwriting it with a sentinel
			// would discard a password somebody just typed.
			continue
		}
		stored[key] = KeyringSentinel
	}
	return stored
}

// LANDevice is one browser that has asked to read the network view.
//
// Approval is per BROWSER, not per machine or per address: a token in a cookie
// is the only thing on a LAN that is actually hard to forge. The consequence is
// honest and worth stating in the UI — clearing cookies, or opening the view in
// a different browser, asks again.
type LANDevice struct {
	// Code is a short human-readable string shown on both screens, so the
	// person approving can tell WHICH device they are approving. Without it,
	// approving from a list of anonymous entries is a coin toss.
	Code string `json:"code"`
	// Approved is the whole gate. A device that has never been approved reads
	// the holding page and nothing else.
	Approved  bool      `json:"approved,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
	// Addr is the address it last connected from, purely so a person can
	// recognise it. Never used to decide anything.
	Addr string `json:"addr,omitempty"`
	// Agent is a coarse guess at the kind of device ("iPhone", "Android"),
	// for the same recognition purpose. Never trusted.
	Agent string `json:"agent,omitempty"`
}
