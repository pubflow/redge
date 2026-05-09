package resp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Value struct {
	Kind  byte
	Str   string
	Int   int64
	Array []Value
}

func Read(r *bufio.Reader, maxBytes int64) (Value, error) {
	prefix, err := r.ReadByte()
	if err != nil {
		return Value{}, err
	}
	switch prefix {
	case '*':
		n, err := readIntLine(r)
		if err != nil {
			return Value{}, err
		}
		if n < 0 {
			return Value{Kind: '*'}, nil
		}
		arr := make([]Value, 0, n)
		var seen int64
		for i := int64(0); i < n; i++ {
			v, err := Read(r, maxBytes-seen)
			if err != nil {
				return Value{}, err
			}
			seen += int64(len(v.Str))
			if seen > maxBytes {
				return Value{}, fmt.Errorf("request too large")
			}
			arr = append(arr, v)
		}
		return Value{Kind: '*', Array: arr}, nil
	case '$':
		n, err := readIntLine(r)
		if err != nil {
			return Value{}, err
		}
		if n < 0 {
			return Value{Kind: '$'}, nil
		}
		if n > maxBytes {
			return Value{}, fmt.Errorf("bulk string too large")
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return Value{}, err
		}
		if !bytes.Equal(buf[n:], []byte("\r\n")) {
			return Value{}, fmt.Errorf("invalid bulk terminator")
		}
		return Value{Kind: '$', Str: string(buf[:n])}, nil
	case '+', '-', ':':
		line, err := readLine(r)
		if err != nil {
			return Value{}, err
		}
		if prefix == ':' {
			n, err := strconv.ParseInt(line, 10, 64)
			if err != nil {
				return Value{}, err
			}
			return Value{Kind: ':', Int: n}, nil
		}
		return Value{Kind: prefix, Str: line}, nil
	default:
		line, err := r.ReadString('\n')
		if err != nil {
			return Value{}, err
		}
		parts := strings.Fields(string(append([]byte{prefix}, []byte(line)...)))
		arr := make([]Value, 0, len(parts))
		for _, p := range parts {
			arr = append(arr, Value{Kind: '$', Str: p})
		}
		return Value{Kind: '*', Array: arr}, nil
	}
}

func readIntLine(r *bufio.Reader) (int64, error) {
	line, err := readLine(r)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(line, 10, 64)
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

func ArrayToStrings(v Value) ([]string, error) {
	if v.Kind != '*' {
		return nil, fmt.Errorf("expected array")
	}
	out := make([]string, 0, len(v.Array))
	for _, item := range v.Array {
		if item.Kind != '$' && item.Kind != '+' {
			return nil, fmt.Errorf("expected bulk string")
		}
		out = append(out, item.Str)
	}
	return out, nil
}

func Simple(s string) []byte { return []byte("+" + s + "\r\n") }
func Error(s string) []byte  { return []byte("-" + s + "\r\n") }
func Int(n int64) []byte     { return []byte(":" + strconv.FormatInt(n, 10) + "\r\n") }

func Bulk(b []byte) []byte {
	return []byte("$" + strconv.Itoa(len(b)) + "\r\n" + string(b) + "\r\n")
}

func NullBulk() []byte { return []byte("$-1\r\n") }

func Array(items ...[]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	for _, item := range items {
		buf.Write(item)
	}
	return buf.Bytes()
}
