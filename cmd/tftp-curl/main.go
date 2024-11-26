package main

import (
	"flag"
	"log"

	tftp "github.com/wjholden/GoTFTPd/internal"
)

var (
	server    = flag.String("server", "", "* mandatory")
	filename  = flag.String("filename", "", "* mandatory")
	blocksize = flag.Int("blocksize", 512, "transfer blocksize")
	timeout   = flag.Int("timeout", 1, "timeout in seconds")
)

// The client here wasn't really planned as the focus of this project. I mostly
// wanted a client to let me write tests, and it seemed like the client and
// server would share enough code that it would be straightforward to get one
// from the other.
//
// Well, it's not quite that simple. TFTP's use of server-side ephemeral ports
// makes things difficult for me. I'm inclined to experiment with a non-standard
// fixed-port for the server.
//
// Anyways, this doesn't work as well as I wanted it to.
func main() {
	var err error
	flag.Parse()

	if *filename == "" {
		flag.Usage()
		return
	}

	c := tftp.TftpClient{}
	err = c.ReadRequest(*server, *filename, *blocksize, *timeout)

	if err != nil {
		log.Fatal(err)
	}
}
