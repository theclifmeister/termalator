package shell

import (
	"fmt"
	"strconv"
	"strings"
)

// batchLine builds the command line that runs the batch file script with
// args through cmd.exe, safe from its parsing: `cmd /d /s /c ""script"
// "arg" …"`. Every argument is in double quotes, which keep & | < > ^ and
// white space literal. cmd expands %…% even inside quotes, so an argument
// with a % is passed as "%TM_ARGn%" and its text in the returned env
// (cmd's expansion happens after its parsing, so the value stays literal).
// An argument with a " or a line break can't be made safe, and is an
// error.
func batchLine(comspec, script string, args []string) (line string, env []string, err error) {
	var b strings.Builder
	b.WriteString(`"` + comspec + `" /d /s /c ""` + script + `"`)
	for i, a := range args {
		if strings.ContainsAny(a, "\"\r\n\x00") {
			return "", nil, fmt.Errorf("argument %d of %s has a quote or line break, which cmd.exe can't pass safely", i+1, script)
		}
		b.WriteString(` "`)
		if strings.Contains(a, "%") {
			name := "TM_ARG" + strconv.Itoa(i)
			env = append(env, name+"="+a)
			b.WriteString("%" + name + "%")
		} else {
			b.WriteString(a)
		}
		b.WriteString(`"`)
	}
	b.WriteString(`"`)
	return b.String(), env, nil
}
