# Contributing

Contributions are welcome: bug reports, fixes, data types that look wrong, setup guides for
platforms not covered here, Grafana dashboards and DuckDB recipes.

- Read [CONTRIBUTING.md](https://github.com/leblancfg/healthkit-relay/blob/main/CONTRIBUTING.md)
  for the development setup and pull request checklist.
- Follow the [Code of Conduct](https://github.com/leblancfg/healthkit-relay/blob/main/CODE_OF_CONDUCT.md).
- Report security issues privately: [SECURITY.md](https://github.com/leblancfg/healthkit-relay/blob/main/SECURITY.md).
- **Never share real health data**, tokens or your server's address in issues, logs or test
  fixtures.

## Working on these docs

The site is [MkDocs Material](https://squidfunk.github.io/mkdocs-material/), built from
`docs/` and `mkdocs.yml`, and published to GitHub Pages on every push to `main`.

```sh
python3 -m venv /tmp/mkdocs
/tmp/mkdocs/bin/pip install -r docs/requirements.txt
/tmp/mkdocs/bin/mkdocs serve           # live preview at http://127.0.0.1:8000
/tmp/mkdocs/bin/mkdocs build --strict  # what CI runs; warnings fail the build
```

Every page has an edit button (the pencil, top right) that opens it on GitHub.

## Repository layout

```text
server/     Go server: receiver, storage, pairing, forwarding
ios/        SwiftUI iPhone app (XcodeGen spec in project.yml)
docs/       this site
flake.nix   Nix package for the server
Makefile    server-test, server-build, ios-build, ios-test
```
