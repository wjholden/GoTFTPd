package main

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

// https://datatracker.ietf.org/doc/html/rfc1350
// https://datatracker.ietf.org/doc/html/rfc1785
// https://datatracker.ietf.org/doc/html/rfc1784
// https://datatracker.ietf.org/doc/html/rfc2347
// https://datatracker.ietf.org/doc/html/rfc2349
func main() {
	Listen()
}

func Listen() {
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

	// identify the request type
	opcode := binary.BigEndian.Uint16(req[0:2])

	req_strings := bytes.Split(req[2:], []byte{0})
	if len(req_strings[len(req_strings)-1]) == 0 {
		req_strings = req_strings[:len(req_strings)-1]
	}

	if len(req_strings) < 2 {
		tftpSendError(fmt.Errorf("request missing filename or mode"), 4, conn)
		return
	}

	filename := string(req_strings[0])
	mode := string(req_strings[1])
	blocksize := 512
	options := make(map[string]string)
	timeout := 1 * time.Second

	if len(req_strings) > 2 {
		fmt.Printf("TFTP request from %s includes options:\n", conn.RemoteAddr().String())
		for i := 2; i < len(req_strings); i += 2 {
			if len(req_strings) < i+2 {
				break
			}
			key := string(req_strings[i])
			value := string(req_strings[i+1])
			fmt.Printf(" - %s = %s", key, value)

			switch key {
			case "blksize":
				blocksize, err = strconv.Atoi(value)
			case "timeout":
				var t int
				t, err = strconv.Atoi(value)
				if err == nil && (t <= 0 || 255 < t) {
					err = fmt.Errorf("timeout %d out of rage [1,255]", t)
				} else {
					timeout = time.Duration(t) * time.Second
				}
			default:
				fmt.Println(" (ignored)")
				continue
			}

			if err != nil {
				tftpSendError(err, 0, conn)
				fmt.Println(" (error)")
			} else {
				options[key] = value
				fmt.Println(" (accepted)")
			}
		}
	}

	if len(options) > 0 {
		tftpSendOptionsAck(&options, conn)

		// RFC 2347: If the transfer was initiated with a Read Request, then an
		// ACK (with the data block	number set to 0) is sent by the client to
		// confirm the values in the server's OACK packet.
		if opcode == 1 {
			zero, err := tftpReceiveAck(conn)
			if zero != 0 {
				err = fmt.Errorf("client did not acknowledge acknlowledged option")
			}
			if err != nil {
				fmt.Println(err)
				return
			}
		}
	}

	switch opcode {
	case 1:
		fmt.Println("read request from", addr.String(), filename, mode)

		err = send(filename, conn, blocksize)
		if err != nil {
			fmt.Println("Error sending:", err)
		}
	case 2:
		fmt.Println("write request from", addr.String())

		err = receive(filename, conn, blocksize, timeout)
		if err != nil {
			fmt.Println("Error receiving:", err)
		}
	default:
		tftpSendError(fmt.Errorf("unexpected opcode (%d)", opcode), 4, conn)
		return
	}
}

func receive(filename string, conn *net.UDPConn, blocksize int, timeout time.Duration) error {
	var prefixed_filename = "./" + filename // force relative path, is this enough?

	if _, err := os.Stat(prefixed_filename); err == nil {
		// file already exists
		err = fmt.Errorf("%s already exists", filename)
		fmt.Println(err)
		tftpSendError(err, 6, conn)
		return err
	}

	start_time := time.Now()
	var blocks_read uint16 = 0
	bytes_read := 0
	tftpSendAck(blocks_read, conn)

	file, err := os.Create(prefixed_filename)
	if err != nil {
		tftpSendError(err, 0, conn)
		return err
	}
	defer file.Close()
	fmt.Printf("Receiving %s...\n", filename)

	hash := md5.New()
	writer := io.MultiWriter(file, hash)

	for {
		// we've acknowledged that we will accept the file, so now receive it.
		read_buffer := make([]byte, blocksize+4)

		conn.SetReadDeadline(time.Now().Add(timeout))
		n, err := conn.Read(read_buffer)

		if n < 0 || err != nil {
			fmt.Println("Read error", err)
			tftpSendError(err, 0, conn)
			continue
		}

		block := binary.BigEndian.Uint16(read_buffer[2:4])

		if block == blocks_read+1 {
			// we read the expected block
			//_, err = f.Write(read_buffer[4:n])
			_, err = io.Copy(writer, bytes.NewReader(read_buffer[4:n]))

			if err != nil {
				fmt.Println(err)
				tftpSendError(err, 0, conn)
				continue
			}

			// we've received, now acknowledge receipt.
			tftpSendAck(block, conn)
			blocks_read++
			bytes_read += n

			if n < blocksize {
				break
			}
		} else if block <= blocks_read {
			// duplicate packet?
			tftpSendAck(block, conn)
			bytes_read += n
			continue
		} else {
			err = fmt.Errorf("received %s block %d, expected %d",
				filename, block, blocks_read+1)
			fmt.Println(err)
			tftpSendError(err, 0, conn)
			continue
		}
	}

	fmt.Println("\033[34m") // blue
	speed_rate, speed_unit := speed(bytes_read, start_time)
	fmt.Printf("Wrote %s from %s (%.2f %s)\n",
		prefixed_filename,
		conn.RemoteAddr().String(),
		speed_rate,
		speed_unit)
	fmt.Println(filename,
		hex.EncodeToString(hash.Sum(nil)))
	fmt.Println("\033[0m") // reset

	return file.Close()
}

func send(filename string, conn *net.UDPConn, blocksize int) error {
	var buf bytes.Buffer

	prefixed_filename := "./" + filename
	file, err := os.Open(prefixed_filename)
	if err != nil {
		tftpSendError(err, 1, conn)
		return err
	}
	defer file.Close()

	start_time := time.Now()
	bytes_sent := 0
	packets_sent := 0
	write_buffer := make([]byte, blocksize)
	hash := md5.New()
	writer := io.MultiWriter(&buf, hash)

	for i := uint16(1); ; {
		n, err := file.ReadAt(write_buffer, int64((i-1))*int64(blocksize))
		if err != nil && err != io.EOF {
			fmt.Printf("I/O problem during transfer: %s.", err)
			tftpSendError(err, 0, conn)
			return err
		}
		buf.Write([]byte{0, 3})                         // TFTP data packet
		buf.Write([]byte{byte(i >> 8), byte(0xff & i)}) // block #
		writer.Write(write_buffer[:n])
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
			break
		}
	}

	fmt.Println("\033[33m") // yellow
	speed_rate, speed_unit := speed(bytes_sent, start_time)
	fmt.Printf("Read %s to %s (%.2f %s)\n",
		filename, conn.RemoteAddr().String(),
		speed_rate, speed_unit)
	fmt.Println(filename,
		hex.EncodeToString(hash.Sum(nil)))
	fmt.Println("\033[0m") // reset color

	return file.Close()
}

func speed(bytes int, start time.Time) (rate float64, unit string) {
	rate = float64(bytes) / time.Since(start).Seconds()
	switch {
	case 1e6 <= rate && rate < 1e9:
		rate /= 1e6
		unit = "Mbps"
	case 1e3 <= rate && rate < 1e6:
		rate /= 1e3
		unit = "kbps"
	default:
		unit = "bps"
	}
	return
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

	if n < 4 {
		err = fmt.Errorf("ack length is not 4 bytes (actual: %d)", n)
		return
	}

	opcode := binary.BigEndian.Uint16(read_buffer[:2])
	switch opcode {
	case 4:
		block = binary.BigEndian.Uint16(read_buffer[2:4])
	case 5:
		err = fmt.Errorf("received TFTP error from %s: %s", conn.RemoteAddr().String(), string(read_buffer[4:]))
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

func tftpSendOptionsAck(options *map[string]string, conn *net.UDPConn) {
	// https://datatracker.ietf.org/doc/html/rfc2347
	var buf bytes.Buffer
	buf.Write([]byte{0, 6})
	for key, value := range *options {
		buf.WriteString(key)
		buf.WriteByte(0)
		buf.WriteString(value)
		buf.WriteByte(0)
	}
	conn.Write(buf.Bytes())
}
