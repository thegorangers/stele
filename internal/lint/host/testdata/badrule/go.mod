// A rule plugin that misbehaves on purpose, one way per MODE. It is its own
// module for the same reason the example is: what it exercises is the
// boundary, and a fake that lived inside the tool would not cross one.
module example.com/stele-rule-bad

go 1.26

require github.com/thegorangers/stele v0.0.0

require google.golang.org/protobuf v1.36.12 // indirect

replace github.com/thegorangers/stele => ../../../../..
