// Command hoex lets an agent in a Docker sandbox run allow-listed commands on
// the host. See docs/PRD.md.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/balesz/hoex/internal/exitcode"
)

func main() {
	mode := flag.String("mode", "", "host | sandbox")
	skill := flag.String("skill", "", "install the agent skill (claude)")
	flag.Parse()

	switch {
	case *skill != "":
		fail("--skill is not implemented yet")
	case *mode == "host":
		fail("--mode host is not implemented yet")
	case *mode == "sandbox":
		fail("--mode sandbox is not implemented yet")
	default:
		fmt.Fprintln(os.Stderr, "usage: hoex --mode host | hoex --mode sandbox -- <command> | hoex --skill claude")
		os.Exit(exitcode.Usage)
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "hoex:", msg)
	os.Exit(exitcode.Internal)
}
