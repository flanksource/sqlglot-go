package tds

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	capturePlaceholder = "example"
	captureUser        = "acme"
	login7FixedSize    = 94
)

var systemDatabases = map[string]bool{"master": true, "tempdb": true, "model": true, "msdb": true}

// scrubber removes credentials and server identity from one connection's messages.
// LOGIN7 is rebuilt, so not even value lengths survive. Other messages keep their
// length and packet boundaries. Traffic after login is otherwise left intact.
type scrubber struct {
	loginSent, loggedIn bool
	identities          []string // server and database names, replaced wherever TDS length-prefixes them
	quoted              []string // also replaced in the login response's message text
}

func (s *scrubber) message(direction string, kind byte, payload []byte) error {
	switch {
	case kind == typeSSPI:
		return errors.New("SSPI authentication cannot be captured")
	case kind == typePrelogin, direction == "server" && !s.loginSent:
		return scrubPrelogin(payload)
	case direction == "server" && !s.loggedIn:
		s.loggedIn = true
		if err := s.learn(payload); err != nil {
			return err
		}
		for _, name := range s.quoted {
			for _, quote := range []string{"'", `"`} {
				replaceSameLength(payload, encodeUTF16LE(quote+name+quote), 2, len(encodeUTF16LE(name)))
			}
		}
		s.replaceIdentities(payload)
	case direction == "server":
		s.replaceIdentities(payload)
	}
	return nil
}

func (s *scrubber) replaceIdentities(payload []byte) {
	for _, name := range s.identities {
		encoded := encodeUTF16LE(name)
		if n := len(encoded) / 2; n <= math.MaxUint8 {
			replaceSameLength(payload, append([]byte{byte(n)}, encoded...), 1, len(encoded))
		}
	}
}

// replaceSameLength overwrites the name inside every match, keeping each offset valid.
func replaceSameLength(payload, match []byte, nameAt, nameLen int) {
	filler := encodeUTF16LE(strings.Repeat(capturePlaceholder, nameLen/len(capturePlaceholder)+1))[:nameLen]
	for from := 0; ; {
		at := bytes.Index(payload[from:], match)
		if at < 0 {
			return
		}
		copy(payload[from+at+nameAt:], filler)
		from += at + len(match)
	}
}

// scrubPrelogin clears the instance name, thread ID and trace ID.
func scrubPrelogin(payload []byte) error {
	for pos := 0; pos < len(payload) && payload[pos] != 0xff; pos += 5 {
		if pos+5 > len(payload) {
			return errors.New("truncated TDS PRELOGIN option")
		}
		offset, size := int(binary.BigEndian.Uint16(payload[pos+1:])), int(binary.BigEndian.Uint16(payload[pos+3:]))
		if offset+size > len(payload) {
			return errors.New("invalid TDS PRELOGIN option bounds")
		}
		if kind := payload[pos]; kind == 2 || kind == 3 || kind == 5 {
			clear(payload[offset : offset+size])
		}
	}
	return nil
}

// login rebuilds LOGIN7 as a single packet with fixed placeholder identities.
func (s *scrubber) login(header, payload []byte) ([][]byte, error) {
	if len(payload) < login7FixedSize {
		return nil, errors.New("short LOGIN7 payload")
	}
	field := func(position int) (string, error) {
		offset, size := int(binary.LittleEndian.Uint16(payload[position:])), 2*int(binary.LittleEndian.Uint16(payload[position+2:]))
		if size == 0 {
			return "", nil
		}
		if offset < login7FixedSize || offset+size > len(payload) {
			return "", fmt.Errorf("invalid LOGIN7 field at %d", position)
		}
		return decodeUTF16LE(payload[offset : offset+size]), nil
	}
	fixed := append([]byte(nil), payload[:login7FixedSize]...)
	clear(fixed[16:20]) // client process ID
	clear(fixed[28:32]) // client time zone
	clear(fixed[36:])   // offsets, client ID and SSPI length are rebuilt below
	var variable []byte
	for _, item := range []struct {
		position int
		value    string
	}{{36, capturePlaceholder}, {40, captureUser}, {44, capturePlaceholder}, {48, capturePlaceholder}, {52, capturePlaceholder}, {60, capturePlaceholder}, {64, ""}, {68, capturePlaceholder}} {
		actual, err := field(item.position)
		if err != nil {
			return nil, err
		}
		value := item.value
		switch item.position {
		case 40:
			s.remember(actual, true)
		case 52, 68:
			s.remember(actual, false)
		case 64:
			value = actual
		}
		if actual == "" {
			value = ""
		}
		encoded := encodeUTF16LE(value)
		if item.position == 44 {
			for i, b := range encoded {
				encoded[i] = (b<<4 | b>>4) ^ 0xa5
			}
		}
		end, chars := login7FixedSize+len(variable), len(encoded)/2
		if end > math.MaxUint16 || chars > math.MaxUint16 {
			return nil, errors.New("LOGIN7 field offset overflow")
		}
		binary.LittleEndian.PutUint16(fixed[item.position:], uint16(end))
		binary.LittleEndian.PutUint16(fixed[item.position+2:], uint16(chars))
		variable = append(variable, encoded...)
	}
	for _, position := range []int{78, 86} {
		if binary.LittleEndian.Uint16(payload[position+2:]) != 0 {
			return nil, errors.New("LOGIN7 SSPI and password change cannot be captured")
		}
	}
	rebuilt := append(fixed, variable...)
	size, total := len(rebuilt), len(rebuilt)+tdsHeaderSize
	if size > math.MaxUint16 || total > math.MaxUint16 {
		return nil, errors.New("LOGIN7 is too large to capture")
	}
	binary.LittleEndian.PutUint32(rebuilt, uint32(size))
	packet := append(append([]byte(nil), header[:tdsHeaderSize]...), rebuilt...)
	packet[1] |= statusEOM
	binary.BigEndian.PutUint16(packet[2:], uint16(total))
	s.loginSent = true
	return [][]byte{packet}, nil
}

func (s *scrubber) remember(name string, onlyQuoted bool) {
	if name == "" || systemDatabases[strings.ToLower(name)] {
		return
	}
	s.quoted = append(s.quoted, name)
	if !onlyQuoted {
		s.identities = append(s.identities, name)
	}
}

// learn collects server and database names from the login response's tokens.
func (s *scrubber) learn(body []byte) error {
	if err := walkTokens(body, s.learnToken); err != nil {
		return fmt.Errorf("login response: %w", err)
	}
	return nil
}

func (s *scrubber) learnToken(token byte, data []byte) error {
	switch token {
	case tokenEnvChange:
		if len(data) == 0 {
			return errors.New("empty login ENVCHANGE")
		}
		switch data[0] {
		case 1, 13: // database, mirroring partner
			for rest := data[1:]; len(rest) > 0; {
				name, next, err := shortString(rest)
				if err != nil {
					return err
				}
				s.remember(name, false)
				rest = next
			}
		case 20:
			return errors.New("login routing cannot be captured")
		}
	case tokenInfo, tokenError:
		_, server, err := tokenMessage(data)
		if err != nil {
			return err
		}
		s.remember(server, false)
	}
	return nil
}

func shortString(data []byte) (string, []byte, error) {
	if len(data) == 0 || 1+2*int(data[0]) > len(data) {
		return "", nil, errors.New("truncated TDS string")
	}
	end := 1 + 2*int(data[0])
	return decodeUTF16LE(data[1:end]), data[end:], nil
}
