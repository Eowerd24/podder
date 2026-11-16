# Security

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/Eowerd24/podder/security/advisories/new) when it is enabled for this repository. If the form is unavailable, ask the maintainer for a private reporting channel without posting exploit details or secrets in a public issue.

Include the affected Podder version, operating system and Podman version, reproduction steps, and expected and observed behavior. Redact credentials, private paths, container environment values, and workload data from logs.

Please check whether the issue reproduces with the latest release. Security fixes may require upgrading; no maintenance window is promised for older releases.

## Validation limits

Automated tests and release verification reduce known risks but do not establish that Podder is free of vulnerabilities. Native behavior varies by operating system and Podman version. Physical power-loss behavior and all platform/runtime combinations have not been validated.

Review the [README](README.md) for workload ownership, build-network consent, filesystem approval, remote access, and recovery limits. Maintainers can find release configuration guidance in [SECURITY-SETUP.md](SECURITY-SETUP.md).
