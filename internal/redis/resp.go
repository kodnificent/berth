package redis

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
)

// maxBulk bounds one bulk string a reply may announce. Redis keeps keys and values up to
// 512 MB, and berth only ever reads key names and short replies.
const maxBulk = 512 << 20

// maxArray bounds the elements one array reply may announce, so a garbled length can't
// make berth allocate without end.
const maxArray = 1 << 24

// reply is one RESP2 reply. Exactly one of its forms is set: Err for an error reply,
// Array for an array, Str for a simple or bulk string, else Int. A null bulk string or
// array is the zero reply.
type reply struct {
	Str   []byte
	Int   int64
	Array []reply
	Err   string
}

// writeCommand sends args as one RESP array of bulk strings, so any byte in a key, a
// pattern or a password reaches the server as it is.
func writeCommand(w *bufio.Writer, args ...[]byte) error {
	fmt.Fprintf(w, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(w, "$%d\r\n", len(a))
		w.Write(a)
		w.WriteString("\r\n")
	}
	return w.Flush()
}

func readReply(r *bufio.Reader) (reply, error) {
	line, err := readLine(r)
	if err != nil {
		return reply{}, err
	}
	if len(line) == 0 {
		return reply{}, fmt.Errorf("the Redis server sent an empty reply line")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return reply{Str: bytes.Clone(body)}, nil // body is the reader's buffer, reused on the next read
	case '-':
		return reply{Err: string(body)}, nil
	case ':':
		n, err := strconv.ParseInt(string(body), 10, 64)
		if err != nil {
			return reply{}, fmt.Errorf("the Redis server sent %q, which is not an integer", body)
		}
		return reply{Int: n}, nil
	case '$':
		n, err := length(body, maxBulk)
		if err != nil || n < 0 {
			return reply{}, err
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return reply{}, err
		}
		if !bytes.Equal(buf[n:], []byte("\r\n")) {
			return reply{}, fmt.Errorf("the Redis server sent a bulk string not ended by CRLF")
		}
		return reply{Str: buf[:n]}, nil
	case '*':
		n, err := length(body, maxArray)
		if err != nil || n < 0 {
			return reply{}, err
		}
		out := reply{Array: make([]reply, 0, min(n, 1024))}
		for range n {
			e, err := readReply(r)
			if err != nil {
				return reply{}, err
			}
			out.Array = append(out.Array, e)
		}
		return out, nil
	}
	return reply{}, fmt.Errorf("the Redis server sent a reply of unknown type %q", line[0])
}

func length(body []byte, limit int) (int, error) {
	n, err := strconv.Atoi(string(body))
	if err != nil || n < -1 || n > limit {
		return 0, fmt.Errorf("the Redis server sent the length %q, which berth can't read", body)
	}
	return n, nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return nil, fmt.Errorf("the Redis server sent a reply line too long to read")
	}
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("the Redis server sent a reply line not ended by CRLF")
	}
	return line[:len(line)-2], nil
}
