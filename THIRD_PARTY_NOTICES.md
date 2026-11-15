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
