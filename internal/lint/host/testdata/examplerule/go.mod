// This module is deliberately not part of the tool's build. It is a rule the
// way somebody else's repository would write one: its own module, depending on
// the published rule package and on nothing under internal/.
//
// The replace directive points at this checkout so the test can build it
// offline. A real rule would depend on a released version of stele instead;
// what is being proved is that the interface can be implemented from outside,
// and that does not change with where the dependency is resolved from.
module example.com/stele-rule-example

go 1.26

require github.com/thegorangers/stele v0.0.0

require google.golang.org/protobuf v1.36.12

replace github.com/thegorangers/stele => ../../../../..
