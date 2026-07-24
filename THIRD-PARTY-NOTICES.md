# Third-Party Notices

backup-maker is licensed under the MIT License (see [LICENSE](LICENSE)).
It links or uses the following third-party software. Full license texts ship
inside each module (see the Go module cache or each project's repository).

## Sync engine (downloaded at runtime, not linked)

| Project | License | Notes |
|---|---|---|
| [Syncthing](https://github.com/syncthing/syncthing) | MPL-2.0 | backup-maker downloads the official, unmodified Syncthing release binary on first run (pinned version, SHA-256 verified) and runs it as a separate process. It is not statically linked into backup-maker. |

## Go modules linked into the backup-maker binary

| Module | License |
|---|---|
| github.com/cloudsoda/go-smb2 | BSD-2-Clause |
| github.com/cloudsoda/sddl | **LGPL-3.0** (see note below) |
| github.com/fsnotify/fsnotify | BSD-3-Clause |
| github.com/geoffgarside/ber | BSD-3-Clause |
| github.com/gofrs/flock | BSD-3-Clause |
| github.com/hashicorp/go-uuid | MPL-2.0 |
| github.com/inconshreveable/mousetrap (Windows builds) | Apache-2.0 |
| github.com/jcmturner/aescts/v2 | Apache-2.0 |
| github.com/jcmturner/dnsutils/v2 | Apache-2.0 |
| github.com/jcmturner/gofork | BSD-3-Clause |
| github.com/jcmturner/goidentity/v6 | Apache-2.0 |
| github.com/jcmturner/gokrb5/v8 | Apache-2.0 |
| github.com/jcmturner/rpc/v2 | Apache-2.0 |
| github.com/pelletier/go-toml/v2 | MIT |
| github.com/spf13/cobra | Apache-2.0 |
| github.com/spf13/pflag | BSD-3-Clause |
| golang.org/x/crypto, /net, /sys, /term | BSD-3-Clause |
| Go standard library | BSD-3-Clause |

### LGPL note

`github.com/cloudsoda/sddl` (an indirect dependency of the SMB client, used
for Windows security-descriptor parsing) is licensed under the GNU Lesser
General Public License v3.0. backup-maker complies with the LGPL's source and
relinking requirements by publishing its complete source code under an
OSI-approved license: anyone can modify the sddl dependency and rebuild the
binary with `go build`. If you redistribute modified binaries without source,
you are responsible for your own LGPL-3.0 compliance with respect to that
library.
