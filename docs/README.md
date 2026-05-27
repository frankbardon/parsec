# Parsec Documentation

This directory holds the mdBook source for the Parsec user manual, published to
GitHub Pages at <https://frankbardon.github.io/parsec/>.

## Local preview

```
$ make docs-serve
```

(Equivalent to `mdbook serve docs --open`.)

## One-shot build

```
$ make docs
```

(Equivalent to `mdbook build docs`. Build output lands in `docs/book/`, which is gitignored. `make docs-clean` removes it.)

## Audience

This site documents the CLI, library embedding in Go, the channel naming
convention, the broker internals, and operations.
