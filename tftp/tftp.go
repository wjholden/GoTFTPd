package tftp

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
	"strings"
	"time"
)

var discard_data bool = false
var read_only bool = false

type ErrorCode uint16

const (
	ERR_UNDEFINED        ErrorCode = 0
	ERR_NOT_FOUND        ErrorCode = 1
	ERR_ACCESS_VIOLATION ErrorCode = 2
	ERR_DISK_FULL        ErrorCode = 3
	ERR_ILLEGAL_OP       ErrorCode = 4
	ERR_UNKNOWN_TID      ErrorCode = 5
	ERR_ALREADY_EXISTS   ErrorCode = 6
	ERR_NO_SUCH_USER     ErrorCode = 7
)

type OpCode uint16

const (
	OPCODE_RRQ   OpCode = 1
	OPCODE_WRQ   OpCode = 2
	OPCODE_DATA  OpCode = 3
	OPCODE_ACK   OpCode = 4
	OPCODE_ERROR OpCode = 5
)

// https://datatracker.ietf.org/doc/html/rfc1350
// https://datatracker.ietf.org/doc/html/rfc1785
// https://datatracker.ietf.org/doc/html/rfc1784
// https://datatracker.ietf.org/doc/html/rfc2347
// https://datatracker.ietf.org/doc/html/rfc2349
func Listen(discard bool, port int, readonly bool) {
	discard_data = discard
	read_only = readonly
	fmt.Println("Starting TFTP server...")
	laddr := &net.UDPAddr{IP: net.IPv6zero, Port: port, Zone: ""}
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

	// The shortest possible TFTP packet is pretty short. It has to contain
	// an opcode (2), filename (at least 1 byte + null terminator), and
	// transfer mode (5 + null terminator).
	if len(req) < 2+1+1+5+1 {
		err = fmt.Errorf("runt request is too short")
		fmt.Fprintln(os.Stderr, err)
		tftpSendError(err, ERR_UNDEFINED, conn)
		return
	}

	opcode := OpCode(binary.BigEndian.Uint16(req[0:2]))

	req_strings := bytes.Split(req[2:], []byte{0})
	if len(req_strings[len(req_strings)-1]) == 0 {
		req_strings = req_strings[:len(req_strings)-1]
	}

	if len(req_strings) < 2 {
		err = fmt.Errorf("request missing filename or mode")
		fmt.Fprintln(os.Stderr, err)
		tftpSendError(err, ERR_ILLEGAL_OP, conn)
		return
	}

	filename := "./" + string(req_strings[0])
	mode := string(req_strings[1])
	blocksize := 512
	timeout := 1 * time.Second
	tsize := 0

	if mode != "octet" {
		err = fmt.Errorf("server only supports octet mode")
		fmt.Fprintln(os.Stderr, err)
		tftpSendError(err, ERR_ILLEGAL_OP, conn)
		return
	}

	options := make(map[string]string)
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
			case "tsize":
				tsize, err = strconv.Atoi(value)
			default:
				fmt.Println(" (ignored)")
				continue
			}

			if err != nil {
				tftpSendError(err, ERR_UNDEFINED, conn)
				fmt.Println(" (error)")
			} else {
				options[key] = value
				fmt.Println(" (accepted)")
			}
		}
	}

	switch opcode {
	case OPCODE_RRQ:
		fmt.Printf("RRQ from %s for %s\n", addr.String(), filename)

		if _, ok := options["tsize"]; ok {
			info, err := os.Stat(filename)
			if err != nil {
				delete(options, "tsize")
			}
			options["tsize"] = strconv.FormatInt(info.Size(), 10)
		}

		tftpSendOptionsAck(&options, opcode, conn)

		err = send(filename, conn, blocksize, timeout)
		if err != nil {
			fmt.Println("Error sending:", err)
		}
	case OPCODE_WRQ:
		if read_only {
			err = fmt.Errorf("this server is read-only")
			tftpSendError(err, ERR_ACCESS_VIOLATION, conn)
			fmt.Println("Rejected WRQ from", addr.String(), "(server is in read-only mode).")
			return
		}

		fmt.Printf("WRQ from %s for %s\n", addr.String(), filename)

		tftpSendOptionsAck(&options, opcode, conn)

		err = receive(filename, conn, blocksize, timeout, tsize)
		if err != nil {
			fmt.Println("Error receiving:", err)
		}
	case OPCODE_DATA, OPCODE_ACK:
		// We've received a data or acknowledgement that isn't consistent
		// with the server's state.
		tftpSendError(fmt.Errorf("who are you?"), ERR_UNKNOWN_TID, conn)
		return
	default:
		tftpSendError(fmt.Errorf("unexpected opcode (%d)", opcode), ERR_ILLEGAL_OP, conn)
		return
	}
}

func receive(filename string, conn *net.UDPConn, blocksize int, timeout time.Duration, tsize int) error {
	if _, err := os.Stat(filename); err == nil {
		// file already exists
		err = fmt.Errorf("%s already exists", filename)
		fmt.Println(err)
		tftpSendError(err, ERR_ALREADY_EXISTS, conn)
		return err
	}

	start_time := time.Now()
	var blocks_read uint16 = 0
	bytes_read := 0
	tftpSendAck(blocks_read, conn)

	var destination io.Writer

	if !discard_data {
		file, err := os.Create(filename)
		if err != nil {
			tftpSendError(err, ERR_UNDEFINED, conn)
			return err
		}

		if err = file.Truncate(int64(tsize)); err != nil {
			// Truncation failed? This should not happen, but maybe the disk is full.
			tftpSendError(err, ERR_UNDEFINED, conn)
			return err
		}

		defer file.Close()
		destination = file
	} else {
		destination = io.Discard
	}

	hash := md5.New()
	writer := io.MultiWriter(destination, hash)

	for {
		// we've acknowledged that we will accept the file, so now receive it.
		read_buffer := make([]byte, blocksize+4)

		conn.SetReadDeadline(time.Now().Add(timeout))
		n, err := conn.Read(read_buffer)

		if n < 0 || err != nil { // timeout
			fmt.Fprint(os.Stderr, "\033[31m") // red
			fmt.Fprint(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "\033[0m") // reset
			//tftpSendError(err, 0, conn)
			// don't send an error. TODO retry some limited # of times.
			continue
		}

		block := binary.BigEndian.Uint16(read_buffer[2:4])

		if block == blocks_read+1 {
			// we read the expected block
			//_, err = f.Write(read_buffer[4:n])
			_, err = io.Copy(writer, bytes.NewReader(read_buffer[4:n]))

			if err != nil {
				fmt.Println(err)
				tftpSendError(err, ERR_UNDEFINED, conn)
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
			tftpSendError(err, ERR_UNDEFINED, conn)
			continue
		}
	}

	fmt.Println("\033[34m") // blue
	speed_rate, speed_unit := speed(bytes_read, start_time)
	var verb string
	if discard_data {
		verb = "Discarded"
	} else {
		verb = "Wrote"
	}
	fmt.Printf("%s %s from %s (%.2f %s)\n",
		verb,
		filename,
		conn.RemoteAddr().String(),
		speed_rate,
		speed_unit)
	fmt.Println(filename,
		hex.EncodeToString(hash.Sum(nil)))
	fmt.Println("\033[0m") // reset

	return nil
}

func send(filename string, conn *net.UDPConn, blocksize int, timeout time.Duration) error {
	var buf bytes.Buffer

	file, err := os.Open(filename)
	if err != nil {
		tftpSendError(err, ERR_NOT_FOUND, conn)
		return err
	}
	defer file.Close()

	start_time := time.Now()
	bytes_sent := 0
	packets_sent := 0
	write_buffer := make([]byte, blocksize)
	hash := md5.New()
	writer := io.MultiWriter(&buf, hash)
	retries := 0

	for i := uint16(1); ; {
		n, err := file.ReadAt(write_buffer, int64((i-1))*int64(blocksize))
		if err != nil && err != io.EOF {
			fmt.Printf("I/O problem during transfer: %s.", err)
			tftpSendError(err, ERR_UNDEFINED, conn)
			return err
		}
		buf.Write([]byte{0, 3})                         // TFTP data packet
		buf.Write([]byte{byte(i >> 8), byte(0xff & i)}) // block #
		writer.Write(write_buffer[:n])
		conn.Write(buf.Bytes())
		buf.Reset()
		bytes_sent += n
		packets_sent++

		conn.SetReadDeadline(time.Now().Add(timeout))
		block, err2 := tftpReceiveAck(conn)
		if err2 != nil {
			if strings.Contains(err2.Error(), "i/o timeout") {
				// Retry the transmission 6 times before giving up.
				if retries < 6 {
					retries++
				} else {
					tftpSendError(err2, ERR_UNDEFINED, conn)
					return err2
				}
			} else if strings.Contains(err2.Error(), "TFTP error") {
				// If we got an explicit TFTP error then stop immediately.
				return err2
			} else {
				// We got some other error that we can try to recover from.
				fmt.Println(err2)
				continue
			}
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

	return nil
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

func tftpSendError(err error, errcode ErrorCode, conn *net.UDPConn) {
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

	opcode := OpCode(binary.BigEndian.Uint16(read_buffer[:2]))
	switch opcode {
	case OPCODE_ACK:
		block = binary.BigEndian.Uint16(read_buffer[2:4])
	case OPCODE_ERROR:
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

func tftpSendOptionsAck(options *map[string]string, opcode OpCode, conn *net.UDPConn) {
	if len(*options) == 0 {
		return
	}

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

	if opcode == OPCODE_RRQ {
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
