package tds

import (
	"encoding/binary"
	"errors"
)

var errFound = errors.New("token found")

// responseError returns the first error message, or "" when the response has none
// before a token that cannot be decoded.
func responseError(body []byte) string {
	var detail string
	_ = walkTokens(body, func(token byte, data []byte) error {
		if token != tokenError {
			return nil
		}
		message, _, err := tokenMessage(data)
		if err != nil {
			return err
		}
		detail = message
		return errFound
	})
	return detail
}

// tokenMessage reads the text and server name of an ERROR or INFO token.
func tokenMessage(data []byte) (message, server string, err error) {
	if len(data) < 8 {
		return "", "", errTruncatedToken
	}
	end := 8 + 2*int(binary.LittleEndian.Uint16(data[6:]))
	if end > len(data) {
		return "", "", errTruncatedToken
	}
	server, _, err = shortString(data[end:])
	return decodeUTF16LE(data[8:end]), server, err
}

// returnHandle returns the prepared statement handle: the first output parameter,
// which follows any result sets the statement produced.
func returnHandle(body []byte) (int32, bool) {
	var handle int32
	err := walkTokens(body, func(token byte, data []byte) error {
		if token != tokenReturnValue {
			return nil
		}
		r := &tokenReader{data: data}
		r.take(2)
		r.byteString()
		r.take(1 + 4 + 2)
		info := r.typeInfo()
		value := r.value(info)
		if r.err != nil || info.kind != 0x26 || len(value) != 4 {
			return errors.New("prepared statement handle is not an int")
		}
		if _, err := binary.Decode(value, binary.LittleEndian, &handle); err != nil {
			return err
		}
		return errFound
	})
	return handle, errors.Is(err, errFound)
}
