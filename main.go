package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
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

	req_strings := bytes.Split(req[2:], []byte{0})
	if len(req_strings[len(req_strings)-1]) == 0 {
		req_strings = req_strings[:len(req_strings)-1]
	}
	fmt.Println(req_strings)

	if len(req_strings) < 2 {
		tftpSendError(fmt.Errorf("request missing filename or mode"), 4, conn)
		return
	}

	filename := string(req_strings[0])
	mode := string(req_strings[1])
	blocksize := 512
	options := make(map[string]string)

	if len(req_strings) > 2 {
		fmt.Printf("TFTP request from %s includes options:\n", conn.RemoteAddr().String())
		for i := 2; i < len(req_strings); i += 2 {
			if len(req_strings) < i+2 {
				break
			}
			key := string(req_strings[i])
			value := string(req_strings[i+1])
			fmt.Printf(" - %s = %s\n", key, value)

			if key == "blksize" {
				blocksize, err = strconv.Atoi(value)

				if err != nil {
					tftpSendError(err, 0, conn)
				} else {
					options[key] = value
				}
			}
		}
	}

	if len(options) > 0 {
		tftpSendOptionsAck(options, conn)
	}

	switch opcode {
	case 1:
		fmt.Println("read request from ", addr.String(), filename, mode)

		_ = send(filename, conn, blocksize)
	case 2:
		fmt.Println("write request from ", addr.String())

		_ = receive(filename, conn, blocksize)
	default:
		tftpSendError(fmt.Errorf("unexpected opcode (%d)", opcode), 4, conn)
		return
	}
}

func receive(filename string, conn *net.UDPConn, blocksize int) error {
	_ = filename

	tftpSendAck(0, conn)

	for {
		// we've acknowledged that we will accept the file, so now receive it.
		read_buffer := make([]byte, blocksize+4)
		n, _ := conn.Read(read_buffer)

		block := binary.BigEndian.Uint16(read_buffer[2:4])
		fmt.Print(string(read_buffer[4:]))

		// we've received, now acknowledge receipt.
		tftpSendAck(block, conn)

		if n < blocksize {
			break
		}
	}

	return nil
}

func send(filename string, conn *net.UDPConn, blocksize int) error {
	var buf bytes.Buffer

	file, err := os.Open(filename)
	if err != nil {
		tftpSendError(err, 1, conn)
		return err
	}
	defer file.Close()

	bytes_sent := 0
	packets_sent := 0
	write_buffer := make([]byte, blocksize)
	for i := uint16(1); ; {
		n, err := file.ReadAt(write_buffer, int64((i-1))*int64(blocksize))
		if err != nil && err != io.EOF {
			fmt.Printf("I/O problem during transfer: %s.", err)
			tftpSendError(err, 0, conn)
			return err
		}
		buf.Write([]byte{0, 3})                         // TFTP data packet
		buf.Write([]byte{byte(i >> 8), byte(0xff & i)}) // block #
		buf.Write(write_buffer[:n])
		conn.Write(buf.Bytes())
		buf.Reset()
		bytes_sent += n
		packets_sent++

		block, err2 := tftpReceiveAck(conn)
		if err2 != nil {
			fmt.Println(err2)
			continue
		}

		if block == i {
			i++
		}

		if n == 0 || err == io.EOF {
			fmt.Printf("Transfer %s to %s complete (%d bytes/%d blocks/%d packets).\n", filename, conn.RemoteAddr().String(), bytes_sent, i-1, packets_sent)
			break
		}
	}

	return nil
}

func tftpSendError(err error, errcode uint16, conn *net.UDPConn) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 5, byte(errcode >> 8), byte(errcode & 0xff)}) // TFTP error packet with no defined error code
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
	buf.Write([]byte{byte(block >> 8), byte(0xff & block)})
	conn.Write(buf.Bytes())
}

func tftpSendOptionsAck(options map[string]string, conn *net.UDPConn) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 6})
	for key, value := range options {
		buf.Write([]byte(key))
		buf.Write([]byte(value))
	}
	conn.Write(buf.Bytes())
	fmt.Println("Options acknowledgement", buf)
}
