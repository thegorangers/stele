package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantErr  string // empty means the run must succeed or ask for help
		help     bool
		wantHelp string // a substring the help text must carry
	}{
		{name: "no arguments prints usage", args: nil, help: true},
		{name: "help", args: []string{"--help"}, help: true},
		{name: "export help", args: []string{"export", "--help"}, help: true},
		{name: "unknown command", args: []string{"nosuch"}, wantErr: `unknown command "nosuch"`},
		{name: "unknown flag is named", args: []string{"export", "--nosuch"}, wantErr: "not defined: -nosuch"},
		{name: "flag before command is named", args: []string{"--nosuch", "export"}, wantErr: `unknown flag "--nosuch"`},
		{name: "output is required", args: []string{"export"}, wantErr: "--output is required"},
		{name: "update is accepted", args: []string{"export", "--update"}, wantErr: "--output is required"},
		{name: "update is documented", args: []string{"export", "--help"}, help: true, wantHelp: "--update"},
		{name: "positional argument refused", args: []string{"export", "--output", "o", "x"}, wantErr: `unexpected argument "x"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			err := run(context.Background(), tc.args, &out, &errOut)
			switch {
			case tc.help:
				if !errors.Is(err, errHelp) {
					t.Fatalf("want help, got %v", err)
				}
				if out.Len() == 0 {
					t.Fatal("help printed nothing")
				}
				if tc.wantHelp != "" && !strings.Contains(out.String(), tc.wantHelp) {
					t.Fatalf("help does not mention %q:\n%s", tc.wantHelp, out.String())
				}
			case tc.wantErr != "":
				if err == nil {
					t.Fatal("want an error, got none")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
			}
		})
	}
}

var _ io.Writer = (*strings.Builder)(nil)
