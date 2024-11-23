package main

import (
	"flag"

	"github.com/wjholden/GoTFTPd/tftp"
)

var (
	discard = flag.Bool("discard", false, "accept transfers but don't actually write them to disk")
)

func main() {
	flag.Parse()
	tftp.Listen(*discard)
}
