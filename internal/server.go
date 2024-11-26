package tftp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"
)

type TftpServer struct {
	TftpNode
	Port int
}

func (s *TftpServer) Listen() {
	laddr := &net.UDPAddr{IP: net.IPv6zero, Port: s.Port, Zone: ""}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	fmt.Println("Started TFTP server on", laddr.String())

	for {
		buf := make([]byte, 1500)
		n, addr, err := conn.ReadFromUDP(buf)

		if err != nil {
			log.Fatal(err)
		}

		buf = buf[0:n]
		go s.handleClient(buf, addr)
	}
}

func (t *TftpNode) handleClient(req []byte, addr *net.UDPAddr) {
	start_time := time.Now()

	s := TftpSession{}
	s.TftpNode = *t

	connection, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	s.conn = connection
	defer s.conn.Close()

	// The shortest possible TFTP packet is pretty short. It has to contain
	// an opcode (2), filename (at least 1 byte + null terminator), and
	// transfer mode (5 + null terminator).
	if len(req) < 2+1+1+5+1 {
		err = fmt.Errorf("runt request")
		fmt.Fprintln(os.Stderr, err)
		s.tftpSendError(err, ERR_UNDEFINED)
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
		s.tftpSendError(err, ERR_ILLEGAL_OP)
		return
	}

	s.Filename = "./" + string(req_strings[0])
	s.mode = string(req_strings[1])
	s.blocksize = 512
	s.timeout = 1 * time.Second
	s.tsize = 0

	if s.mode != "octet" && s.mode != "binary" {
		err = fmt.Errorf("server only supports octet mode") // but we will also accept "binary"
		fmt.Fprintln(os.Stderr, err)
		s.tftpSendError(err, ERR_ILLEGAL_OP)
		return
	}

	options := make(map[string]string)
	if len(req_strings) > 2 {
		//fmt.Printf("TFTP request from %s includes options:\n", connection.RemoteAddr().String())
		for i := 2; i < len(req_strings); i += 2 {
			if len(req_strings) < i+2 {
				break
			}
			key := string(req_strings[i])
			value := string(req_strings[i+1])
			//fmt.Printf(" - %s = %s", key, value)

			switch key {
			case "blksize":
				s.blocksize, err = strconv.Atoi(value)
			case "timeout":
				var t int
				t, err = strconv.Atoi(value)
				if err == nil && (t <= 0 || 255 < t) {
					err = fmt.Errorf("timeout %d out of rage [1,255]", t)
				} else {
					s.timeout = time.Duration(t) * time.Second
				}
			case "tsize":
				s.tsize, err = strconv.Atoi(value)
			default:
				//fmt.Println(" (ignored)")
				continue
			}

			if err != nil {
				s.tftpSendError(err, ERR_UNDEFINED)
				//fmt.Println(" (error)")
			} else {
				options[key] = value
				//fmt.Println(" (accepted)")
			}
		}
	}

	switch opcode {
	case OPCODE_RRQ:
		fmt.Printf("RRQ from %s for %s\n", addr.String(), s.Filename)

		if _, ok := options["tsize"]; ok {
			info, err := os.Stat(s.Filename)
			if err != nil {
				delete(options, "tsize")
			}
			options["tsize"] = strconv.FormatInt(info.Size(), 10)
		}

		s.tftpSendOptionsAck(&options, opcode)

		err = s.send()
		if err != nil {
			fmt.Println("Error sending:", err)
			return
		}

		fmt.Println("\033[33m") // yellow
		speed_rate, speed_unit := speed(s.bytes, start_time)
		fmt.Printf("Read %s to %s (%.2f %s)\n",
			s.Filename, s.conn.RemoteAddr().String(),
			speed_rate, speed_unit)
		fmt.Println(s.Filename,
			hex.EncodeToString(s.hash.Sum(nil)))
		fmt.Println("\033[0m") // reset color
	case OPCODE_WRQ:
		fmt.Printf("WRQ from %s for %s\n", addr.String(), s.Filename)

		if t.ReadOnly {
			err = fmt.Errorf("this server is read-only")
			s.tftpSendError(err, ERR_ACCESS_VIOLATION)
			fmt.Println("Rejected WRQ from", addr.String(), "(server is in read-only mode).")
			return
		}

		if _, err := os.Stat(s.Filename); err == nil {
			// file already exists
			err = fmt.Errorf("%s already exists", s.Filename)
			fmt.Fprintln(os.Stderr, err)
			s.tftpSendError(err, ERR_ALREADY_EXISTS)
		}

		s.tftpSendOptionsAck(&options, opcode)

		s.tftpSendAck(0)

		err := s.receive()
		if err != nil {
			fmt.Println("Error receiving:", err)
			return
		}

		// log report and MD5 sum
		fmt.Println("\033[34m") // blue
		speed_rate, speed_unit := speed(s.bytes, start_time)
		var verb string
		if s.DiscardData {
			verb = "Discarded"
		} else {
			verb = "Wrote"
		}
		fmt.Printf("%s %s from %s (%.2f %s)\n",
			verb,
			s.Filename,
			s.conn.RemoteAddr().String(),
			speed_rate,
			speed_unit)
		fmt.Println(s.Filename, hex.EncodeToString(s.hash.Sum(nil)))
		fmt.Println("\033[0m") // reset
	case OPCODE_DATA, OPCODE_ACK:
		// We've received a data or acknowledgement that isn't consistent
		// with the server's state.
		s.tftpSendError(fmt.Errorf("who are you?"), ERR_UNKNOWN_TID)
		return
	default:
		s.tftpSendError(fmt.Errorf("unexpected opcode (%d)", opcode), ERR_ILLEGAL_OP)
		return
	}
}
