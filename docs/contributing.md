# Contributing

Contributions are welcome: bug reports, fixes, data types that look wrong, setup guides for
more platforms, Grafana dashboards and DuckDB recipes.

- Read [CONTRIBUTING.md](https://github.com/leblancfg/healthkit-relay/blob/main/CONTRIBUTING.md)
  for the development setup and pull request checklist.
- Follow the [Code of Conduct](https://github.com/leblancfg/healthkit-relay/blob/main/CODE_OF_CONDUCT.md).
- Report security issues privately: [SECURITY.md](https://github.com/leblancfg/healthkit-relay/blob/main/SECURITY.md).
- **Keep real health data**, tokens and your server address out of issues, logs and test
  fixtures. Replace them with made-up values such as `http://192.168.1.10:8080`.

## Working on these docs

The site is [MkDocs Material](https://squidfunk.github.io/mkdocs-material/), built from
`docs/` and `mkdocs.yml`. GitHub Pages publishes it on every push to `main` that changes
them. To preview it, from the repository's root folder:

1. Make a Python environment with MkDocs in it:

    ```sh
    python3 -m venv /tmp/mkdocs
    /tmp/mkdocs/bin/pip install -r docs/requirements.txt
    ```

2. Start the live preview:

    ```sh
    /tmp/mkdocs/bin/mkdocs serve
    ```

    Open `http://127.0.0.1:8000`. The page reloads each time you save a file.

3. Before you open a pull request, build it the way CI does:

    ```sh
    /tmp/mkdocs/bin/mkdocs build --strict
    ```

    It ends with `Documentation built`; any warning fails the build.

Every page has an edit button (the pencil, top right) that opens it on GitHub. Words such
as *server address* or *read token* have one meaning each, in the [glossary](glossary.md):
use them as defined there.

## Repository layout

```text
server/     Go server: receiver, archive (SQLite staging, Parquet), pairing, query API, forwarding
ios/        SwiftUI iPhone app (XcodeGen spec in project.yml)
docs/       this site
flake.nix   Nix package, NixOS module (nix/) and its VM test
packaging/  systemd unit shipped in the release archives
scripts/    smoke-test.sh, the end-to-end check every install path runs in CI
Makefile    server-test, server-build, ios-build, ios-test
```
