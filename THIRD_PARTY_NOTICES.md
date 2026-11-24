# Third-party software

Remote Dev Kit is independent of Anthropic and Tailscale. Accounts and service
terms remain with their respective providers. No account, private key or node
identity is included in the source or release packages.

- Tailscale open-source CLI/daemon: v1.104.7, BSD-3-Clause. Official module
  `tailscale.com`. Release builds compile the unmodified upstream sources with
  `-trimpath`; runtime state is created separately on the installing device.
- github.com/pkg/sftp v1.13.11: BSD-2-Clause.
- github.com/coder/websocket v1.8.15: ISC.
- Dependencies retain their own licenses. Release archives include collected
  LICENSE/COPYING/NOTICE files in `licenses/`.
- Chrome/Chromium, Claude Code, macFUSE/SSHFS and Node.js are not bundled. Their
  installers or downloads are optional and retain their original licenses.
- The optional browser MCP command uses Google's
  `chrome-devtools-mcp@1.13.0`, installed separately with npm on the remote host.

Package checksums detect corruption; they do not replace publisher signature
verification. Initial alpha builds are not Developer ID/notarized or Authenticode
signed. The setup scripts do not disable operating-system protections.

## Independent Plan B

The Plan B orchestration, device stream protocol, SSH executor and web utility
are implemented in Remote Dev Kit. They do not incorporate cc-desk-tunnel source,
application, protocol or runtime. That project was a design reference only.

- github.com/creack/pty v1.1.24: MIT, https://github.com/creack/pty .
- xterm.js (@xterm/xterm 6.0.0), @xterm/addon-fit 0.11.0: MIT,
  https://github.com/xtermjs/xterm.js . These are terminal renderer libraries,
  not an agent runtime. Their notices are retained in `web/plan-b/vendor/` and
  embedded with the page; the npm tarballs were checked against SHA-512 integrity.
- Optional remote images install official Claude Code separately from npm,
  currently @anthropic-ai/claude-code 2.1.321; its original terms apply. The CLI is
  not distributed in rdev archives. Node is part of the optional remote image.
- github.com/hashicorp/yamux v0.1.2: MPL-2.0,
  https://github.com/hashicorp/yamux/tree/v0.1.2 . The unmodified library provides
  reliable stream multiplexing inside the private SSH transport. Its source and
  MPL notice remain available upstream and its license is collected in releases.
