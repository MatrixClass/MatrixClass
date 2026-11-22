package remoteexec

import "strings"

// splitCommandLine splits an argument string into fields while honouring
// quotes, so that values such as `"C:\Program Files\x.txt" -v` stay intact.
//
// When posix is false (Windows), only double quotes group text; a backslash is
// literal except when it escapes a double quote (`\"`). When posix is true
// (Linux), single quotes group text literally, and outside single quotes a
// backslash escapes the following character.
func splitCommandLine(value string, posix bool) []string {
	var (
		args     []string
		cur      strings.Builder
		inArg    bool
		inDouble bool
		inSingle bool
	)
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\\' && i+1 < len(runes) && (posix || runes[i+1] == '"'):
			if posix && inDouble && !strings.ContainsRune("\"\\$`", runes[i+1]) {
				// Inside POSIX double quotes, backslash only escapes a few chars.
				cur.WriteRune(r)
				inArg = true
				continue
			}
			i++
			cur.WriteRune(runes[i])
			inArg = true
		case r == '"':
			inDouble = !inDouble
			inArg = true
		case r == '\'' && posix && !inDouble:
			inSingle = true
			inArg = true
		case !inDouble && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}
