package api

import "errors"

// errNotImpl is returned by every App method while the corresponding backend
// dependency is not yet wired in (Phase 1a contract-only mode). It lets the
// frontend import and compile against the typed bindings before the store /
// service implementations exist.
var errNotImpl = errors.New("backend not yet implemented")
