// Command stele-rule-bad is a rule plugin that fails on purpose.
//
// Every way a rule in another process can let this tool down is here, one per
// MODE, because each of them must produce a message that names the rule and
// says what to do — and the only way to know that is to have one do it.
package main

import (
	"bufio"
	"os"
	"time"

	"github.com/thegorangers/stele/rule"
)

func main() {
	mode := os.Getenv("MODE")
	out := bufio.NewWriter(os.Stdout)

	switch mode {
	case "silent":
		// Starts, says nothing, exits. The commonest shape of a rule whose
		// main function forgot to call Serve.
		return
	case "chatty":
		// Writes something that is not a frame at all before anything else —
		// a stray print, or a logger pointed at stdout.
		os.Stdout.WriteString("stele-rule-bad version 0.1\n")
		return
	}

	id := "bad/" + mode
	switch mode {
	case "reserved_namespace":
		id = "stele/no_todo"
	case "reserved_aip_namespace":
		id = "aip/158_list_request_page_size"
	}
	hello := rule.Hello{Protocol: rule.Protocol, Rules: []rule.Descriptor{{
		ID: id, Description: "fails on purpose, in the manner named by MODE",
	}}}
	if err := rule.WriteFrame(out, hello); err != nil {
		os.Exit(1)
	}
	out.Flush()

	in := bufio.NewReader(os.Stdin)
	for {
		var req rule.Request
		if err := rule.ReadFrame(in, &req); err != nil {
			return
		}
		switch mode {
		case "crash":
			os.Stderr.WriteString("bad: index out of range\n")
			os.Exit(2)
		case "hang":
			time.Sleep(time.Hour)
		case "garbage":
			os.Stdout.WriteString("not a frame, just something a rule printed\n")
			time.Sleep(time.Minute)
		case "no_fix":
			rule.WriteFrame(out, rule.Response{Findings: []rule.WireFinding{{
				Line: 3, Message: "something is wrong",
			}}})
		case "refuses":
			rule.WriteFrame(out, rule.Response{Error: "the descriptor set holds no source information"})
		default:
			rule.WriteFrame(out, rule.Response{})
		}
		out.Flush()
	}
}
