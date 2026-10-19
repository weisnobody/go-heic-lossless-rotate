# go-heic-lossless-rotate

Losslessly rotate HEIC images by modifying metadata.

A Go port of [heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate)
by Oleg Ostrozhansky. It's meant both as a **drop-in replacement** for the Python
CLI (same commands, flags, messages, and exit codes, and byte-identical output
files) and as an **importable Go library** whose functions mirror the
Python source one to one. It has no dependencies outside the Go standard library.

> **Status: pre-release.** The project scaffolding and test foundation are in
> place. The rotation core, CLI, and binaries aren't implemented yet; see
> [docs/PLAN.md](docs/PLAN.md) for progress. Until they land, use the
> [Python tool](https://github.com/olegoz/heic-lossless-rotate).

Implements upstream **v1.7.1** (see [docs/UPSTREAM.md](docs/UPSTREAM.md)).

## Development

Requires Go 1.23+. The differential suite also needs Python 3.11+ and git.

```bash
make test       # unit tests (no Python needed)
make lint       # go vet (+ staticcheck if installed)
make build      # compile; binaries land in ./bin/ once cmd/ exists
make difftest   # fetch the pinned upstream into .upstream/ and compare Go vs Python
```

`make difftest` compares this port against the real Python
implementation at the exact upstream commit in
[`difftest/upstream.commit`](difftest/upstream.commit), and requires their output to be
byte-identical. See [`difftest/doc.go`](difftest/doc.go) for its environment
variables.

Drop real `.heic` files into `testdata/` to include them in round-trip
tests. The directory's contents are gitignored.

## Documentation

- [docs/SPEC.md](docs/SPEC.md): what "drop-in" guarantees, and the scope
- [docs/DESIGN.md](docs/DESIGN.md): architecture and key decisions
- [docs/PLAN.md](docs/PLAN.md): implementation roadmap
- [docs/UPSTREAM.md](docs/UPSTREAM.md): pinned upstream version and the Python → Go function map
- [docs/COMPAT.md](docs/COMPAT.md): every deliberate difference from the Python tool

## License

MIT. See [LICENSE](LICENSE). Portions are derived from heic-lossless-rotate,
Copyright (c) 2026 Oleg Ostrozhansky.
