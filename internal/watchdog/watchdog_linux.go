// SPDX-License-Identifier: MIT

//go:build linux

package watchdog

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// writeTimeout bounds one keepalive. A datagram to systemd does not block in
// practice; the deadline is here so that the goroutine whose whole job is to
// notice a wedge cannot itself become one.
const writeTimeout = 2 * time.Second

// open connects to the service manager's notification socket, if this process
// is under a watchdog at all.
//
// A nil Notifier with a nil error is the ordinary "no watchdog here" answer:
// run from a terminal, or under a unit with no WatchdogSec=. An error means a
// watchdog WAS asked for and cannot be fed, which is worth shouting about.
func open() (Notifier, time.Duration, error) {
	usec := os.Getenv("WATCHDOG_USEC")
	if usec == "" {
		return nil, 0, nil
	}
	// systemd names the process it expects the pings from. Anything else that
	// inherited this environment — the sync engine we spawn, most obviously —
	// must keep quiet, exactly as sd_watchdog_enabled() decides it.
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return nil, 0, nil
	}
	interval, err := parseUsec(usec)
	if err != nil {
		return nil, 0, err
	}
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil, 0, fmt.Errorf("WATCHDOG_USEC is set but NOTIFY_SOCKET is not; there is nowhere to send keepalives")
	}
	conn, err := dialNotify(addr)
	if err != nil {
		return nil, 0, err
	}
	return conn, interval, nil
}

// parseUsec reads systemd's $WATCHDOG_USEC, which is microseconds.
func parseUsec(s string) (time.Duration, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("WATCHDOG_USEC=%q is not a number: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("WATCHDOG_USEC=%q is not a usable interval", s)
	}
	return time.Duration(n) * time.Microsecond, nil
}

// dialNotify opens the sd_notify datagram socket named by $NOTIFY_SOCKET.
//
// systemd spells an ABSTRACT socket — one in the kernel's abstract namespace,
// with no file behind it — with a leading "@". The real name starts with a NUL
// byte instead, so that is what we substitute. Both forms occur in the wild: a
// user manager typically hands out a path under /run/user/UID.
func dialNotify(addr string) (Notifier, error) {
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing the service manager socket %q: %w", addr, err)
	}
	return &notifySocket{conn: conn}, nil
}

// notifySocket is the sd_notify protocol, which for our purposes is one line of
// text per datagram and no reply.
type notifySocket struct{ conn net.Conn }

func (s *notifySocket) Ping() error {
	if err := s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	_, err := s.conn.Write([]byte("WATCHDOG=1\n"))
	return err
}

func (s *notifySocket) Close() error { return s.conn.Close() }
