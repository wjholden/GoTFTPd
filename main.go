package main

import (
	"flag"

	"github.com/wjholden/GoTFTPd/tftp"
)

var (
	discard  = flag.Bool("discard", false, "accept transfers but don't actually write them to disk")
	port     = flag.Int("port", 69, "UDP port to listen on")
	readonly = flag.Bool("readonly", false, "reject all writes")
)

func main() {
	flag.Parse()
	s := tftp.TftpNode{Port: *port, DiscardData: *discard, ReadOnly: *readonly}
	s.Listen()
}
