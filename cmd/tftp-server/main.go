package main

import (
	"flag"

	tftp "github.com/wjholden/GoTFTPd/internal"
)

var (
	discard  = flag.Bool("discard", false, "accept transfers but don't actually write them to disk")
	port     = flag.Int("port", 69, "UDP port to listen on")
	readonly = flag.Bool("readonly", false, "reject all writes")
)

func main() {
	flag.Parse()
	s := tftp.TftpServer{TftpNode: tftp.TftpNode{
		DiscardData: *discard,
		ReadOnly:    *readonly},
		Port: *port}
	s.Listen()
}
