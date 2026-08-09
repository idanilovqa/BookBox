# Managed Hybrid Autostart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist and safely reconcile the exact Container or Hybrid deployment selected for Windows logon autostart.

**Architecture:** Extend the existing owned Compose state helper with exact same-state reconciliation, then thread the mode and overlay through the discovery installer, scheduled action, manifest, startup, and remover. Keep Docker scope derived from `Resolve-BookBoxComposeScope`; Hybrid never supplies `container-worker`.

**Tech Stack:** Windows PowerShell 5.1, Docker Compose v2, Windows Scheduled Tasks, repository static/behavioral PowerShell contracts.

## Global Constraints

- Preserve exact path, PID, scheduled-task, firewall, and Compose ownership checks.
- Reject malformed or mismatched state before Docker mutation.
- Never delete volumes, configuration, secrets, or media.
- Run PowerShell test processes sequentially.

---

### Task 1: Exact same-state Compose reconciliation

**Files:**
- Modify: `deploy/scripts/compose-lifecycle.ps1`
- Modify: `deploy/scripts/hybrid-compose.tests.ps1`

**Interfaces:**
- Produces: `Ensure-BookBoxComposeState -StateDirectory -Scope -Address -Port -EnvironmentFile`, returning an object with `Created` and validated `Record` fields.

- [ ] Add a failing behavioral contract that accepts an identical Hybrid record and rejects changed mode, files, profiles, environment, address, or port.
- [ ] Run `powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/hybrid-compose.tests.ps1` and confirm the missing ensure operation fails.
- [ ] Implement exact state comparison while retaining strict `Write-BookBoxComposeState` behavior.
- [ ] Rerun the contract and confirm it passes.

### Task 2: Mode-owned scheduled logon startup

**Files:**
- Modify: `deploy/scripts/install-bookbox-discovery.ps1`
- Modify: `deploy/scripts/start-bookbox.ps1`
- Modify: `deploy/scripts/remove-bookbox-discovery.ps1`
- Modify: `deploy/scripts/discovery-process.tests.ps1`
- Modify: `deploy/scripts/startup-environment.tests.ps1`

**Interfaces:**
- Installer consumes `-DeploymentMode Container|Hybrid` and `-HybridComposeFile <path>`.
- Scheduled action passes the canonical mode, base file, overlay, state directory, endpoint, and discovery executable.
- Startup consumes `Ensure-BookBoxComposeState`; exact existing discovery state is a no-op after validation.

- [ ] Add failing static and behavioral contracts for Hybrid action arguments, manifest fields, `IgnoreNew`, `up -d --remove-orphans`, exact live-discovery reuse, and stop-before-remove ordering.
- [ ] Run the focused discovery/startup contracts and record the expected failures.
- [ ] Implement the installer, startup, and remover changes with rollback ownership based on `Created`.
- [ ] Rerun the focused contracts and confirm they pass.

### Task 3: Operator documentation and final verification

**Files:**
- Modify: `docs/installation.md`
- Modify: `docs/setup-server.md`
- Modify: `docs/operations.md`

- [ ] Add explicit `-DeploymentMode Hybrid -HybridComposeFile ...` installation examples and repeated-logon behavior.
- [ ] Run, sequentially: `hybrid-compose.tests.ps1`, `discovery-process.tests.ps1`, `startup-environment.tests.ps1`, and `build-release-process.tests.ps1`.
- [ ] Run `git diff --check` and independently review the scoped diff.
- [ ] Commit the implementation on the current branch.
