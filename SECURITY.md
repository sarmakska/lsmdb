# Security Policy

I take the integrity of lsmdb seriously. It is a storage engine, so a defect
that corrupts data or loses an acknowledged write is treated with the same
urgency as a classic security vulnerability.

## Reporting a vulnerability

Please report any vulnerability privately by emailing security@sarmalinux.com.
Include a description of the issue, the version or commit you tested, and a
reproduction if you have one. Please do not open a public issue for a security
report until I have had a chance to investigate and ship a fix.

I commit to acknowledging your report within 7 days and to keeping you updated
as I work through triage, a fix and a coordinated disclosure. If I confirm the
issue I will credit you in the changelog unless you ask me not to.

## Supported versions

I provide security fixes for the latest minor release line. lsmdb is pre-1.0,
so the support window tracks the most recent tagged release.

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | Yes                |
| < 0.1   | No                 |

## Scope

In scope: data corruption, loss of acknowledged writes, crash-recovery
failures, panics reachable from the public API with valid inputs, and
out-of-bounds reads while parsing an SSTable or write-ahead log.

Out of scope: behaviour when the on-disk files are modified by another process
while the database is open, and resource exhaustion driven by deliberately
unbounded inputs from a trusted caller.
