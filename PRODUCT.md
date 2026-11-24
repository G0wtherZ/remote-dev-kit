# Remote Dev Kit

<!-- impeccable:product-schema 1 -->

## Platform

web

The main product is a cross-platform command line tool; web is an optional companion.

## Users

Developers using local macOS or Windows with a remote Linux/macOS environment, including local files and USB hardware.

## Product Purpose

Keep the agent Harness remotely operated while making local/remote collaboration simple and explicit.

## Operating Context

Existing SSH and Tailscale connections; users may already run another VPN. Native Claude Code remains separately managed. The optional browser view reconnects to native terminals rather than replacing their conversation UI.

## Capabilities and Constraints

Default A executes remotely with scoped local access. Optional B executes as the local user after explicit enable. B must be independently implemented in this repository; cc-desk-tunnel is a design reference only. Preserve existing Claude accounts/history, existing VPS websites, VPN routes and DNS. Reset never deletes user data.

## Product Principles

- Simple, convenient, efficient local/remote collaboration.
- Guided setup, explicit access and recoverable lifecycle.
- Keep native agent interaction and optional agent installation.
- Test real behavior and distinguish model/account/platform evidence.
