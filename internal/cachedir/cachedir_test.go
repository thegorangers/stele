package cachedir

import (
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		name                string
		override, xdg, home string
		want                string
		wantErr             bool
	}{
		{name: "override wins", override: "/o", xdg: "/x", home: "/h", want: "/o"},
		{name: "xdg", xdg: "/x", home: "/h", want: filepath.Join("/x", "stele")},
		{name: "home", home: "/h", want: filepath.Join("/h", ".cache", "stele")},
		{name: "nothing", wantErr: true},
		{name: "relative xdg is ignored", xdg: "rel", home: "/h", want: filepath.Join("/h", ".cache", "stele")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tc.override, tc.xdg, tc.home)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
