package remoteexec

import (
	"reflect"
	"testing"
)

func TestSplitCommandLineWindows(t *testing.T) {
	cases := map[string][]string{
		``:                                    nil,
		`  -a   -b `:                          {"-a", "-b"},
		`"C:\Program Files\x.txt" /v`:         {`C:\Program Files\x.txt`, "/v"},
		`--name="a b" C:\dir\`:                {"--name=a b", `C:\dir\`},
		`say \"hi\" O'Brien`:                  {"say", `"hi"`, "O'Brien"},
		`"" x`:                                {"", "x"},
		`"unterminated value`:                 {"unterminated value"},
		"tab\tseparated":                      {"tab", "separated"},
		`--path "D:\Some Dir\file name.docx"`: {"--path", `D:\Some Dir\file name.docx`},
	}
	for in, want := range cases {
		if got := splitCommandLine(in, false); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommandLine(%q, false) = %#v, want %#v", in, got, want)
		}
	}
}

func TestSplitCommandLinePOSIX(t *testing.T) {
	cases := map[string][]string{
		`--open '/home/u/My Docs/a.txt'`: {"--open", "/home/u/My Docs/a.txt"},
		`"a b" c\ d`:                     {"a b", "c d"},
		`"say \"x\" \n"`:                 {`say "x" \n`},
		`'it''s'`:                        {"its"},
	}
	for in, want := range cases {
		if got := splitCommandLine(in, true); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommandLine(%q, true) = %#v, want %#v", in, got, want)
		}
	}
}
