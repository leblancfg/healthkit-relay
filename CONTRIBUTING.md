# Contributing to Health Relay

Thanks for helping. Bug reports, fixes, setup guides for platforms we have not covered,
Grafana dashboards and DuckDB recipes are all welcome. By taking part you agree to the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- **Bugs and features:** open an issue with the templates. For anything larger than a small
  fix, discuss it in an issue first so nobody builds something that cannot be merged.
- **Security issues:** never in public; see [SECURITY.md](SECURITY.md).
- **Never paste real health data** in issues, pull requests, logs or test fixtures. Use
  synthetic samples. Scrub server addresses and tokens from logs and screenshots.

## Development

### Server (`server/`)

Go (version in `server/go.mod`), no C toolchain needed.

```sh
make server-test     # go vet + go test
make server-build    # static binary in bin/healthkit-relay
make server-run      # local try-out: data in ./tmp, a throwaway token
```

With Nix: `nix build` builds the server, `nix develop` or `nix shell nixpkgs#go` gives you Go.

### iPhone app (`ios/`)

Needs macOS, Xcode, [XcodeGen](https://github.com/yonaskolb/XcodeGen) and a paid Apple
Developer account to run on a device (the simulator works for UI tests without one).

```sh
cp ios/Config/Local.xcconfig.example ios/Config/Local.xcconfig   # your team and bundle id
make ios-project     # generate the Xcode project from ios/project.yml
make ios-test        # unit and UI tests on a simulator
```

Edit `ios/project.yml`, not the generated project, `Info.plist` or entitlements. Never commit
`Local.xcconfig`. See [ios/README.md](ios/README.md).

### Docs (`docs/`)

```sh
python3 -m venv /tmp/mkdocs && /tmp/mkdocs/bin/pip install -r docs/requirements.txt
/tmp/mkdocs/bin/mkdocs serve          # http://127.0.0.1:8000
/tmp/mkdocs/bin/mkdocs build --strict # what CI runs
```

## Pull requests

- One change per pull request, with tests for server behaviour you add or fix.
- `make server-test` and `mkdocs build --strict` pass.
- Keep the server a single static binary with no runtime services to install. New
  dependencies need a reason.
- Keep the wire format backward compatible: older apps must keep working against a newer
  server. Changes to the API or storage get a note in `server/README.md` and the changelog
  (`docs/changelog.md`).
- Privacy is a feature: no analytics, telemetry, third-party SDKs or network destinations
  other than the user's server.
- Commit messages: a short summary line prefixed with the area (`server:`, `ios:`, `docs:`),
  then the why.

Contributions are licensed under the [MIT license](LICENSE).
