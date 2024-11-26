package tftp

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

type TftpClient struct {
	TftpSession
	Server string
}

func (c *TftpClient) Transfer(opcode OpCode) (err error) {
	// Start a server socket to listen on from all sources
	conn1, _ := net.ListenUDP("udp", nil)
	defer conn1.Close()
	laddr, _ := net.ResolveUDPAddr("udp", conn1.LocalAddr().String())

	// Dial the server on their port 69 (or whatever)
	raddr1, _ := net.ResolveUDPAddr("udp", c.Server)
	//conn2, err := net.DialUDP("udp", laddr, raddr1)
	//if err != nil {
	//	return
	//}
	//defer conn2.Close()

	// Send the RRQ/WRQ packet.
	var buf bytes.Buffer
	buf.Write([]byte{0, byte(opcode)})
	buf.WriteString(c.Filename)
	buf.WriteByte(0)
	buf.WriteString("octet")
	buf.WriteByte(0)
	//conn2.Write(buf.Bytes())
	conn1.WriteToUDP(buf.Bytes(), raddr1)

	// The server should respond on an ephemeral port.
	// We are going to lose this packet, but the server should re-send it.
	readbuf := make([]byte, 1500)
	_, raddr2, _ := conn1.ReadFromUDP(readbuf)

	// Now that we know the server's TID we can actually dial them from our
	// "server" port.
	conn1.Close()
	conn3, err := net.DialUDP("udp", laddr, raddr2)
	if err != nil {
		log.Fatal(err)
	}
	defer conn3.Close()
	c.conn = conn3

	fmt.Println(conn3.LocalAddr().String(), conn3.RemoteAddr().String())

	// Actually do the transfer. The names look reversed because we're reusing
	// server code.
	switch opcode {
	case OPCODE_RRQ:
		err = c.receive()
	case OPCODE_WRQ:
		err = c.send()
	}
	return
}

func (c *TftpClient) ReadRequest(server string, filename string, blocksize int, timeout int) (err error) {
	c.output = os.Stdout
	c.Server = server
	c.Filename = filename
	c.blocksize = blocksize
	c.timeout = time.Duration(timeout) * time.Second
	return c.Transfer(OPCODE_RRQ)
}
