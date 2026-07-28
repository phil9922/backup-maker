// SPDX-License-Identifier: MIT

package syncthing

// Thin structs holding only the fields backup-maker reads or writes. Config
// updates are done read-modify-write per section so fields we don't model
// survive round-trips (we re-marshal the raw JSON we received).

type Folder struct {
	ID              string         `json:"id"`
	Label           string         `json:"label"`
	Path            string         `json:"path"`
	Type            string         `json:"type"` // sendonly | receiveonly | sendreceive
	Devices         []FolderDevice `json:"devices"`
	FsWatcherEnab   bool           `json:"fsWatcherEnabled"`
	FsWatcherDelayS float64        `json:"fsWatcherDelayS"`
	RescanIntervalS int            `json:"rescanIntervalS"`
	Paused          bool           `json:"paused"`
	Versioning      Versioning     `json:"versioning"`
}

type FolderDevice struct {
	DeviceID string `json:"deviceID"`
}

type Versioning struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

type Device struct {
	DeviceID          string   `json:"deviceID"`
	Name              string   `json:"name"`
	Addresses         []string `json:"addresses"`
	Paused            bool     `json:"paused"`
	AutoAcceptFolders bool     `json:"autoAcceptFolders"`
}

// Options mirrors the subset of syncthing's options block we enforce.
type Options struct {
	ListenAddresses       []string `json:"listenAddresses"`
	GlobalAnnounceEnabled bool     `json:"globalAnnounceEnabled"`
	LocalAnnounceEnabled  bool     `json:"localAnnEnabled"`
	RelaysEnabled         bool     `json:"relaysEnabled"`
	NatEnabled            bool     `json:"natEnabled"`
	URAccepted            int      `json:"urAccepted"`
	CrashReportingEnabled bool     `json:"crashReportingEnabled"`
	StartBrowser          bool     `json:"startBrowser"`
}

type SystemStatus struct {
	MyID string `json:"myID"`
}

type Connections struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
		// StartedAt is when the SYNC SESSION began, which is not the same as
		// having a connection: a device we have reached but that has not
		// approved us keeps connected=true with a zero startedAt
		// ("0001-01-01T00:00:00Z") for as long as the handshake keeps being
		// rejected. That difference is the only honest way to tell "waiting to
		// be approved over there" from "switched off".
		StartedAt string `json:"startedAt"`
	} `json:"connections"`
}

type DeviceStats struct {
	LastSeen string `json:"lastSeen"`
}

type FolderStatus struct {
	State        string `json:"state"`
	StateChanged string `json:"stateChanged"`
	GlobalFiles  int    `json:"globalFiles"`
	GlobalBytes  int64  `json:"globalBytes"`
	NeedFiles    int    `json:"needFiles"`
	NeedBytes    int64  `json:"needBytes"`
	// ReceiveOnly* describe local drift on a receive-only folder: files that
	// were changed on THIS machine and so differ from what the machine that
	// sends the backup holds.
	//
	// ReceiveOnlyTotalItems is the only honest count of them, and the one to
	// show a user. Measured against syncthing 2.1.2 on a folder where one
	// received file had been edited, one deleted and one added locally:
	// changedFiles was 2 (edited + added, deletions EXCLUDED), changedDeletes
	// was 1, totalItems was 3. Reporting changedFiles would have told the user
	// two things differed when three did.
	ReceiveOnlyChangedFiles   int   `json:"receiveOnlyChangedFiles"`
	ReceiveOnlyChangedDeletes int   `json:"receiveOnlyChangedDeletes"`
	ReceiveOnlyChangedBytes   int64 `json:"receiveOnlyChangedBytes"`
	ReceiveOnlyTotalItems     int   `json:"receiveOnlyTotalItems"`
	Errors                    int   `json:"errors"`
}

type Completion struct {
	Completion  float64 `json:"completion"`
	NeedItems   int     `json:"needItems"`
	NeedBytes   int64   `json:"needBytes"`
	NeedDeletes int     `json:"needDeletes"`
	RemoteState string  `json:"remoteState"`
}

type PendingDevices map[string]struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Time    string `json:"time"`
}

// PendingFolders maps folder ID -> offering devices.
type PendingFolders map[string]struct {
	OfferedBy map[string]struct {
		Label string `json:"label"`
		Time  string `json:"time"`
	} `json:"offeredBy"`
}

type Event struct {
	ID   int64          `json:"id"`
	Type string         `json:"type"`
	Time string         `json:"time"`
	Data map[string]any `json:"data"`
}
