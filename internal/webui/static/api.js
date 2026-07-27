// SPDX-License-Identifier: MIT

// mutate() is fetch() for anything that changes something on this machine.
// Every non-GET call in the dashboard goes through it; reads stay on plain
// fetch().
//
// The X-Backup-Maker header is the whole point of it. A page on another origin
// cannot set a custom header without a CORS preflight, and the daemon answers
// no preflight and sends no CORS headers at all, so the browser never sends the
// real request. (A form POST cannot set headers at all.) This page can set it
// freely, because it is served BY the daemon and so is genuinely same-origin —
// including over an ssh -L tunnel, where the port differs from the one the
// daemon bound but the page and the API still share one origin.
//
// That is what stands between a backup and a web page on some other port of
// this same machine, which the browser would otherwise let approve a pairing
// request with the real session cookie attached. The daemon refuses any
// state-changing request that arrives without the header, so a call left on
// plain fetch() is a button that returns 403.
function mutate(url, opts) {
  const o = { ...opts };
  o.headers = { ...o.headers, 'X-Backup-Maker': '1' };
  return fetch(url, o);
}
