package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
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

		_ = send(filename, conn)
	case 2:
		fmt.Println("write request from ", addr.String())

		_ = receive(filename, conn)
	}
}

func receive(filename string, conn *net.UDPConn) error {
	_ = filename

	tftpSendAck(0, conn)

	for {
		// we've acknowledged that we will accept the file, so now receive it.
		read_buffer := make([]byte, 1500)
		n, _ := conn.Read(read_buffer)

		block := binary.BigEndian.Uint16(read_buffer[2:4])
		fmt.Print(string(read_buffer[4:]))

		// we've received, now acknowledge receipt.
		tftpSendAck(block, conn)

		if n < 512+2+2 {
			break
		}
	}

	return nil
}

func send(filename string, conn *net.UDPConn) error {
	var buf bytes.Buffer

	file, err := os.Open(filename)
	if err != nil {
		tftpSendError(err, conn)
		return err
	}
	defer file.Close()

	write_buffer := make([]byte, 512)
	for i := uint16(1); ; {
		n, err := file.ReadAt(write_buffer, int64((i-1)*512))
		if err != nil && err != io.EOF {
			tftpSendError(err, conn)
			return err
		}
		buf.Write([]byte{0, 3})                                  // TFTP data packet
		buf.Write([]byte{byte(0xff & (i >> 8)), byte(0xff & i)}) // block #
		buf.Write(write_buffer[:n])
		conn.Write(buf.Bytes())
		buf.Reset()

		block, err := tftpReceiveAck(conn)
		if err != nil {
			fmt.Println(err)
			continue
		} else if block == i {
			i++
		}

		if n == 0 || err == io.EOF {
			break
		}
	}

	return nil
}

func tftpSendError(err error, conn *net.UDPConn) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 5, 0, 0}) // TFTP error packet with no defined error code
	buf.Write([]byte(fmt.Sprint(err)))
	conn.Write(buf.Bytes())
}

func tftpReceiveAck(conn *net.UDPConn) (block uint16, err error) {
	read_buffer := make([]byte, 1500)
	n, _, err := conn.ReadFrom(read_buffer)
	if err != nil {
		return
	}

	if n != 4 {
		err = fmt.Errorf("ack length is not 4 bytes (actual: %d)", n)
		return
	}

	opcode := binary.BigEndian.Uint16(read_buffer[:2])
	switch opcode {
	case 4:
		block = binary.BigEndian.Uint16(read_buffer[2:4])
	case 5:
		err = fmt.Errorf(string(read_buffer[4:]))
	default:
		err = fmt.Errorf("unexpected opcode=%d", opcode)
	}

	return
}

func tftpSendAck(block uint16, conn *net.UDPConn) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 4})
	buf.Write([]byte{byte(0xff & (block >> 8)), byte(0xff & block)})
	conn.Write(buf.Bytes())
}
