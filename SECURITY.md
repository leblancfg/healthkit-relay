# Security policy

Health Relay moves some of the most personal data there is, so security reports are very
welcome and handled first.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately through GitHub:
**[Report a vulnerability](https://github.com/leblancfg/healthkit-relay/security/advisories/new)**
(the repository's *Security* tab › *Report a vulnerability*). Only the maintainers see it.

Include what you can: the affected component (server, iPhone app, docs/examples), version or
commit, steps to reproduce, and the impact you expect. Never include real health data; a
synthetic sample is enough.

What to expect:

- an acknowledgement within 7 days;
- an assessment and a plan (or a reasoned "not a vulnerability") within 30 days;
- a fix released as soon as it is ready, with a GitHub Security Advisory and credit to you
  unless you prefer otherwise.

## Supported versions

Only the latest release of the server and the latest app version get security fixes. Run
the newest release.

## Scope

In scope, for example: authentication or token handling, pairing and setup codes, the setup
page (CSRF, XSS, clickjacking), data written outside the data directory, a way to read or
delete health data without a valid token, the app sending data anywhere other than the
configured server, denial of service by an unauthenticated client.

Out of scope: a server exposed to the internet over plain HTTP against the documentation,
attacks that need the admin token or root on the server, a compromised or jailbroken iPhone,
and issues in Apple's HealthKit itself (report those to Apple).

See [Security & privacy](https://health-relay.leblancfg.com/security/) for the
threat model.
