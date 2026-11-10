# ADR 0196: Independent browser network access

- Status: Accepted
- Date: 2026-11-06
- Scope: Agent browser network preferences, runtime admission and desktop controls
- Updates: [ADR 0195](0195-agent-browser-site-level-authorization.md)

## Problem

Ask and Auto browser sessions require an operator-supplied list of exact origins.
A page can need several origins for redirects, images and scripts, so opening an
ordinary website requires configuration that users cannot reasonably predict.
Full access removes that list requirement while also changing unrelated command
and file permissions. The browser needs its own network choice.

## Decision

The browser offers an **Allow internet** switch, available independently of the
task's Ask, Auto or Full approval mode. A collapsed advanced control optionally
restricts access to specified websites. The normal browser card does not display
an origin list.

The host stores three states: `off`, `internet` and `restricted`. Internet mode
permits ordinary HTTP(S) browser destinations, including local development
servers, redirects and cross-origin page resources. It is not a public-IP-only
firewall. Restricted mode retains ADR 0195's exact-origin and destination checks.
Turning access off retains a configured website list so enabling it again does
not silently remove the restriction.

The operator's choice is stored in the local database, with a project preference
for new conversations and a revisioned policy for the current run. Repository
files and model tool arguments do not grant network access. New projects start
with access off. Existing runs with no explicit policy retain their previous
interpretation until the operator changes it.

Changing browser access invalidates the affected browser sessions and pending
browser approvals. It does not change the task's approval mode, filesystem
access or command permissions. Follow-up runs inherit the conversation's policy;
new conversations use the saved project preference.

Network access and effect approval remain separate. Existing upload, download
release, sensitive-action and intercepted HTTP write approvals continue to apply.
An approval for an old browser generation cannot authorize a later request.
Chromium Fetch interception covers HTTP requests; WebSocket handshakes and
messages are outside that interception. This is an existing limitation of both
restricted and unrestricted browser sessions, reproduced on Windows Edge during
this change, and must not be described as covered by HTTP write approval.

## Verification

Focused checks cover project persistence and run inheritance, revision conflicts,
browser-session invalidation, Ask/Auto internet admission, cross-origin reads and
redirects, restricted-site denial, and the existing HTTP request approval flow.
The frontend is exercised against the production API, with screenshots of the
browser card and permission settings before and after the change. The PR records
the exact checks run and platform coverage.
