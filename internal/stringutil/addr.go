package stringutil

import (
	"bytes"
	"net/mail"
)

var crlf = []byte{13, 10}

// JoinAddress formats a slice of Address structs such that they can be used in a To or Cc header.
func JoinAddress(addrs []mail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	buf := &bytes.Buffer{}
	for i, a := range addrs {
		if i > 0 {
			_, _ = buf.WriteString(", ")
		}
		_, _ = buf.WriteString(a.String())
	}
	return buf.String()
}

// EncodeAwareJoinAddress() formats a slice of Address structs such that they can be used in a To or Cc header.
// It respects the provided encoding scheme in contrast to JoinAddress()
func EncodeAwareJoinAddress(headerEncoder func(int, string) (int, string, error), startColumn int, addrs []mail.Address) (string, error) {
	if len(addrs) == 0 {
		return "", nil
	}
	col := startColumn
	buf := &bytes.Buffer{}
	for i, a := range addrs {
		if i > 0 {
			_, _ = buf.WriteRune(',')
			col += 1
			if col > 76 {
				// fold
				_, _ = buf.Write(crlf)
				col = 0
			}
			_, _ = buf.WriteRune(' ')
			col += 1
		}
		if a.Name != "" {
			col, encoded, err := headerEncoder(col, a.Name)
			if err != nil {
				return "", err
			}
			_, _ = buf.WriteString(encoded)
			if col > 76 {
				// fold
				_, _ = buf.Write(crlf)
				col = 0
			}
			_, _ = buf.WriteRune(' ')
			col += 1
		}
		addrPart := (&mail.Address{Address: a.Address}).String()
		_, _ = buf.WriteString(addrPart)
		col += len(addrPart)
	}
	return buf.String(), nil
}

func StringizeAddress(headerEncoder func(int, string) (int, string, error), startColumn int, addr mail.Address) (string, error) {
	return EncodeAwareJoinAddress(headerEncoder, startColumn, []mail.Address{addr})
}
