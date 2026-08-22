Golden files in this directory were not written from documentation. They were
produced by `go run ./internal/managed/extract` against real generated Go code
whose descriptors carry file options that appear in no source .proto, and then
anonymised: the file names and proto packages were replaced with neutral ones,
leaving the shape of every value intact.

Each `.golden` holds the text form of a `FileOptions` message, in the field
order the extractor printed it.
