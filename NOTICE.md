# Notice

This repository is a maintained fork of [floatdrop/2q](https://github.com/floatdrop/2q)
(archived). Its two former dependencies have been folded directly into this
module as `internal/lru` and `internal/fifo`, so the module no longer depends
on anything outside the Go standard library:

- [floatdrop/lru](https://github.com/floatdrop/lru) (archived) — MIT License,
  Copyright (c) Vsevolod Strukchinsky. Code incorporated into `internal/lru`.
- [floatdrop/fifo](https://github.com/floatdrop/fifo) (archived) — MIT License,
  Copyright (c) Vsevolod Strukchinsky. Code incorporated into `internal/fifo`.

Both of the above depended on
[bahlo/generic-list-go](https://github.com/bahlo/generic-list-go) — BSD-3-Clause
License, itself a generics port of Go's `container/list`
(Copyright (c) 2009 The Go Authors). Its code is not copied here; instead
`internal/ring` reimplements the same doubly-linked-ring-with-sentinel idea
directly on a preallocated slice with integer indices instead of pointers, so
it is credited as the design origin rather than as vendored source.
