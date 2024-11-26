package tftp

// https://datatracker.ietf.org/doc/html/rfc1350
// https://datatracker.ietf.org/doc/html/rfc1785
// https://datatracker.ietf.org/doc/html/rfc1784
// https://datatracker.ietf.org/doc/html/rfc2347
// https://datatracker.ietf.org/doc/html/rfc2349

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

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

type TftpNode struct {
	DiscardData bool
	ReadOnly    bool
}

type TftpSession struct {
	TftpNode
	Filename    string
	conn        *net.UDPConn
	blocksize   int
	timeout     time.Duration
	tsize       int
	mode        string
	output      io.Writer
	blocks_read uint16
	bytes       int
	hash        hash.Hash
}

func (s *TftpSession) receive() error {
	if !s.DiscardData {
		if s.output == nil {
			file, err := os.Create(s.Filename)
			if err != nil {
				s.tftpSendError(err, ERR_UNDEFINED)
				return err
			}

			if err = file.Truncate(int64(s.tsize)); err != nil {
				// Truncation failed? This should not happen, but maybe the disk is full.
				s.tftpSendError(err, ERR_UNDEFINED)
				return err
			}

			defer file.Close()
			s.output = file
		}
	} else {
		s.output = io.Discard
	}

	s.hash = md5.New()
	writer := io.MultiWriter(s.output, s.hash)

	for {
		// we've acknowledged that we will accept the file, so now receive it.
		read_buffer := make([]byte, s.blocksize+4)

		s.conn.SetReadDeadline(time.Now().Add(s.timeout))
		n, err := s.conn.Read(read_buffer)

		if n < 0 || err != nil { // timeout
			fmt.Fprint(os.Stderr, "\033[31m") // red
			fmt.Fprint(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "\033[0m") // reset
			// DON'T send an error.
			continue
		}

		block := binary.BigEndian.Uint16(read_buffer[2:4])

		if block == s.blocks_read+1 {
			// we read the expected block
			_, err = io.Copy(writer, bytes.NewReader(read_buffer[4:n]))

			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				s.tftpSendError(err, ERR_UNDEFINED)
				continue
			}

			// we've received, now acknowledge receipt.
			s.tftpSendAck(block)
			s.blocks_read++
			s.bytes += n

			if n < s.blocksize {
				break
			}
		} else if block <= s.blocks_read {
			// duplicate packet?
			s.tftpSendAck(block)
			s.bytes += n
			continue
		} else {
			err = fmt.Errorf("received %s block %d, expected %d",
				s.Filename, block, s.blocks_read+1)
			fmt.Fprintln(os.Stderr, err)
			//s.tftpSendError(err, ERR_UNDEFINED)
			// Don't send an error here. Instead, send an ack of the last valid block.
			s.tftpSendAck(s.blocks_read)
			continue
		}
	}

	return nil
}

func (s *TftpSession) send() error {
	var buf bytes.Buffer

	file, err := os.Open(s.Filename)
	if err != nil {
		s.tftpSendError(err, ERR_NOT_FOUND)
		return err
	}
	defer file.Close()

	write_buffer := make([]byte, s.blocksize)
	s.hash = md5.New()
	writer := io.MultiWriter(&buf, s.hash)
	retries := 0
	i := uint16(1)

	for {
		n, err := file.ReadAt(write_buffer, int64((i-1))*int64(s.blocksize))
		if err != nil && err != io.EOF {
			fmt.Printf("I/O problem during transfer: %s.", err)
			s.tftpSendError(err, ERR_UNDEFINED)
			return err
		}
		buf.Write([]byte{0, byte(OPCODE_DATA)})         // TFTP data packet
		buf.Write([]byte{byte(i >> 8), byte(0xff & i)}) // block #
		writer.Write(write_buffer[:n])
		s.conn.Write(buf.Bytes())
		buf.Reset()
		s.bytes += n

		s.conn.SetReadDeadline(time.Now().Add(s.timeout))
		block, err2 := s.tftpReceiveAck()
		if err2 != nil {
			if strings.Contains(err2.Error(), "i/o timeout") {
				// Retry the transmission 6 times before giving up.
				if retries < 6 {
					retries++
				} else {
					s.tftpSendError(err2, ERR_UNDEFINED)
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
			s.blocks_read = block
			break
		}
	}

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

func (s *TftpSession) tftpSendError(err error, errcode ErrorCode) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 5, byte(errcode >> 8), byte(errcode & 0xff)}) // TFTP error packet with no defined error code
	buf.Write([]byte(fmt.Sprint(err)))
	s.conn.Write(buf.Bytes())
}

func (s *TftpSession) tftpReceiveAck() (block uint16, err error) {
	read_buffer := make([]byte, 1500)
	n, _, err := s.conn.ReadFrom(read_buffer)
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
		err = fmt.Errorf("received TFTP error from %s: %s", s.conn.RemoteAddr().String(), string(read_buffer[4:]))
	default:
		err = fmt.Errorf("unexpected opcode=%d", opcode)
	}

	return
}

func (s *TftpSession) tftpSendAck(block uint16) {
	var buf bytes.Buffer
	buf.Write([]byte{0, 4})
	buf.Write([]byte{byte(block >> 8), byte(0xff & block)})
	s.conn.Write(buf.Bytes())
}

func (s *TftpSession) tftpSendOptionsAck(options *map[string]string, opcode OpCode) {
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
	s.conn.Write(buf.Bytes())

	if opcode == OPCODE_RRQ {
		zero, err := s.tftpReceiveAck()
		if zero != 0 {
			err = fmt.Errorf("client did not acknowledge acknlowledged option")
		}
		if err != nil {
			fmt.Println(err)
			return
		}
	}
}
