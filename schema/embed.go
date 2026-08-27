// Package schema holds the JSON Schemas that describe stele's configuration
// files, and nothing else.
//
// The schemas exist so that an editor can offer completion and inline errors
// in stele.yaml, stele.lock and stele.baseline before anything is run. They live in this
// repository, beside the parser, and are held to it: schema_test.go puts one
// corpus of example files through both this schema and the real parser and
// fails on any disagreement, in either direction. A schema maintained
// separately from the code it describes is a document that drifts; this one
// cannot drift without a red test.
//
// The files are also plain JSON on disk, so a user can point at them by URL or
// by path without going through Go at all. See the README.
package schema

import _ "embed"

// ManifestJSON is the schema of stele.yaml.
//
//go:embed stele.schema.json
var ManifestJSON []byte

// LockJSON is the schema of stele.lock.
//
//go:embed stele.lock.schema.json
var LockJSON []byte

// BaselineJSON is the schema of stele.baseline.
//
//go:embed stele.baseline.schema.json
var BaselineJSON []byte
