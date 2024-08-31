package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"
)

func main() {
	fmt.Println("Starting TFTP server...")
	laddr := &net.UDPAddr{IP: net.IPv6zero, Port: 69, Zone: ""}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	for {
		buf := make([]byte, 1500)
		n, addr, err := conn.ReadFromUDP(buf)

		fmt.Println("received a client: ", addr)

		if err != nil {
			log.Fatal(err)
		}

		buf = buf[0:n]
		go handleClient(buf, addr)
	}
}

func handleClient(req []byte, addr *net.UDPAddr) {
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// acknowledge the client before we start transmitting
	//enc.Encode(TftpAckPacket{4, 0})

	// identify the request type
	opcode := binary.BigEndian.Uint16(req[0:2])

	filename_end := bytes.IndexByte(req[2:], 0)
	if filename_end == -1 {
		log.Fatal("Invalid filename")
	}
	filename := string(req[2 : filename_end+2])
	mode := string(req[filename_end+2:])

	switch opcode {
	case 1:
		fmt.Println("read request from ", addr.String(), filename, mode)

		var buf bytes.Buffer

		buf.Write([]byte{0, 3}) // TFTP data packet
		buf.Write([]byte{0, 1}) // block #1
		buf.WriteString("Hello world!")
		conn.Write(buf.Bytes())
	case 2:
		fmt.Println("write request from ", addr.String())

		var buf bytes.Buffer

		buf.Write([]byte{0, 4}) // TFTP ack packet
		buf.Write([]byte{0, 0}) // block #0
		conn.Write(buf.Bytes())

		for {
			// we've acknowledged that we will accept the file, so now receive it.
			read_buffer := make([]byte, 1500)
			n, _ := conn.Read(read_buffer)

			block := binary.BigEndian.Uint16(read_buffer[2:4])
			fmt.Printf("Read block %d (%d bytes): ", block, n)
			fmt.Println(string(read_buffer[4:]))

			// we've received, now acknowledge receipt.
			buf.Reset()
			buf.Write([]byte{0, 4})
			buf.Write([]byte{byte(0xff & (block >> 8)), byte(0xff & block)})
			conn.Write(buf.Bytes())
			fmt.Println(buf.Bytes())

			if n < 512+2+2 {
				break
			}
		}
	}
}
