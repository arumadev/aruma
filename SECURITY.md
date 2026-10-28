# Security policy

Aruma is a backend that holds other people's data and credentials, so we take security reports seriously.

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub: open the **Security** tab of this repository and choose **Report a vulnerability**. Include what you found, how to reproduce it, and which version or commit you tested.

We will acknowledge the report, confirm or dispute it, and keep you updated until it is fixed. Please give us a reasonable time to fix the problem before you disclose it.

## Supported versions

Aruma is pre-1.0. Security fixes go to the latest release and to `main`.

## Scope

In scope: the server, the console, the Management API, the SDK integration packages under `sdk/`, and the default configuration.

Out of scope: problems in Firebase or in Google's own SDKs, and findings that depend on a deliberately weakened configuration.

## What we already do

The secure defaults are written down in ADR-010, and the threat model is in [docs/security/threat-model.md](docs/security/threat-model.md).
