# Managed Hybrid Autostart Design

## Goal

Make the installed BookBox discovery logon task start the exact deployment mode chosen at installation, with Hybrid mode never enabling the container catalog worker. Repeated logon triggers must reconcile an already-valid deployment instead of failing or starting a second discovery process.

## Installed ownership contract

`install-bookbox-discovery.ps1` accepts `DeploymentMode` (`Container` or `Hybrid`) and `HybridComposeFile`. It canonicalizes the base and hybrid Compose paths, requires the overlay for Hybrid mode, rejects unsafe scheduled-task argument characters, and records the mode, paths, endpoint, and exact scheduled action in the owned manifest. The scheduled action passes both `-DeploymentMode` and `-HybridComposeFile` to `start-bookbox.ps1`. Its task policy uses `MultipleInstances = IgnoreNew`.

An existing version-1 discovery installation is not silently rewritten because its action may default to unsafe Container mode. The installer accepts only an exact current owned task/manifest or requires the operator to remove the old installation first.

## Idempotent startup

The Compose lifecycle helper gains an ensure operation. With no state it atomically writes the requested exact scope and reports that this invocation owns the new state. With existing state it validates exact equality of deployment mode, ordered canonical Compose files, profiles, env-file choice, address, and port, then reports that the deployment already existed. Malformed, mismatched, or tampered state is preserved and rejected before Docker is invoked.

`start-bookbox.ps1` always reconciles the validated exact scope with `docker compose up -d --remove-orphans`. Hybrid uses base plus overlay and no `container-worker` profile. If the exact recorded discovery process is alive, startup treats it as already running; stale state is retired through the existing identity-safe lifecycle before one replacement is launched. Rollback removes only resources created by the current invocation.

## Managed removal

Removal validates the owned task and manifest, disables/stops the exact task to prevent a concurrent trigger, and uses the installed state-aware stop script before deleting owned files or firewall rules. If scoped Compose shutdown fails, removal refuses to delete ownership records. Configuration, secrets, database volumes, and media remain preserved.

## Verification

PowerShell contracts cover exact Hybrid task arguments, manifest ownership, `IgnoreNew`, same-state idempotence, mismatch rejection before Docker, one discovery process, and state-driven removal ordering. Existing Container mode, Hybrid Compose, startup environment, discovery lifecycle, and release-process contracts remain green.
