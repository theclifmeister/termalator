// Command hello is the smallest cgo link check for libghostty-vt; it is also
// the baseline for binary-size numbers in FINDINGS.md.
package main

import (
	"fmt"
	"log"

	"go.mitchellh.com/libghostty"
)

func main() {
	term, err := libghostty.NewTerminal(libghostty.WithSize(80, 24))
	if err != nil {
		log.Fatal(err)
	}
	defer term.Close()
	fmt.Fprintf(term, "Hello, \033[1;32mworld\033[0m! 👋 漢字\r\n")
	f, err := libghostty.NewFormatter(term, libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain), libghostty.WithFormatterTrim(true))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	s, _ := f.FormatString()
	fmt.Println(s)
}
