package tds

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// RPC procedure identifiers and type layout follow Mori's ext_handler.go.
var procedures = map[uint16]string{1: "sp_cursor", 2: "sp_cursoropen", 3: "sp_cursorprepare", 4: "sp_cursorexecute", 5: "sp_cursorprepexec", 6: "sp_cursorunprepare", 7: "sp_cursorfetch", 8: "sp_cursoroption", 9: "sp_cursorclose", 10: "sp_executesql", 11: "sp_prepare", 12: "sp_execute", 13: "sp_prepexec", 14: "sp_prepexecrpc", 15: "sp_unprepare"}

func decodeRPC(body []byte) (string, []any, error) {
	if len(body) < 6 {
		return "", nil, errors.New("truncated TDS RPC")
	}
	offset := int(binary.LittleEndian.Uint32(body))
	if offset < 4 || offset+4 > len(body) {
		return "", nil, errors.New("invalid RPC ALL_HEADERS")
	}
	body = body[offset:]
	n := int(binary.LittleEndian.Uint16(body))
	body = body[2:]
	var name string
	if n == 0xffff {
		if len(body) < 4 {
			return "", nil, errors.New("truncated RPC identifier")
		}
		id := binary.LittleEndian.Uint16(body)
		name = procedures[id]
		body = body[2:]
		if name == "" {
			name = fmt.Sprintf("procedure#%d", id)
		}
	} else {
		if n == 0 || n*2+2 > len(body) {
			return "", nil, errors.New("invalid RPC procedure name")
		}
		name = decodeUTF16LE(body[:n*2])
		body = body[n*2:]
	}
	body = body[2:]
	var args []any
	for len(body) > 0 {
		n := int(body[0])*2 + 2
		if n >= len(body) {
			return "", nil, errors.New("truncated RPC parameter")
		}
		body = body[n:]
		value, used, err := parameter(body)
		if err != nil {
			return "", nil, err
		}
		args = append(args, value)
		body = body[used:]
	}
	return strings.ToLower(name), args, nil
}

func parameter(body []byte) (any, int, error) {
	if len(body) < 2 {
		return nil, 0, errors.New("truncated RPC type")
	}
	kind := body[0]
	switch kind {
	case 0x26, 0x68, 0x6d:
		if len(body) < 3 {
			break
		}
		n := int(body[2])
		if len(body) < 3+n {
			break
		}
		if n == 0 {
			return nil, 3, nil
		}
		b := body[3 : 3+n]
		if kind == 0x68 && n == 1 {
			return b[0] != 0, 4, nil
		}
		if kind == 0x6d {
			if n == 4 {
				return math.Float32frombits(binary.LittleEndian.Uint32(b)), 7, nil
			}
			if n == 8 {
				return math.Float64frombits(binary.LittleEndian.Uint64(b)), 11, nil
			}
		}
		if kind == 0x26 {
			switch n {
			case 1:
				return int32(b[0]), 4, nil
			case 2:
				var value int16
				if _, err := binary.Decode(b, binary.LittleEndian, &value); err != nil {
					return nil, 0, err
				}
				return int32(value), 5, nil
			case 4:
				var value int32
				if _, err := binary.Decode(b, binary.LittleEndian, &value); err != nil {
					return nil, 0, err
				}
				return value, 7, nil
			case 8:
				var value int64
				if _, err := binary.Decode(b, binary.LittleEndian, &value); err != nil {
					return nil, 0, err
				}
				return value, 11, nil
			}
		}
	case 0xe7, 0xa7, 0xa5:
		offset := 3
		if kind != 0xa5 {
			offset += 5
		}
		if len(body) < offset+2 {
			break
		}
		if binary.LittleEndian.Uint16(body[1:]) == 0xffff {
			return plp(body, offset, kind)
		}
		n := int(binary.LittleEndian.Uint16(body[offset:]))
		offset += 2
		if n == 0xffff {
			return nil, offset, nil
		}
		if offset+n > len(body) {
			break
		}
		if kind == 0xe7 {
			if n%2 != 0 {
				break
			}
			return decodeUTF16LE(body[offset : offset+n]), offset + n, nil
		}
		if kind == 0xa7 {
			return string(body[offset : offset+n]), offset + n, nil
		}
		return append([]byte(nil), body[offset:offset+n]...), offset + n, nil
	default:
		return nil, 0, fmt.Errorf("unsupported RPC parameter type 0x%x", kind)
	}
	return nil, 0, errors.New("invalid RPC parameter length or encoding")
}
func plp(body []byte, offset int, kind byte) (any, int, error) {
	if offset+8 > len(body) {
		return nil, 0, errors.New("truncated PLP length")
	}
	total := binary.LittleEndian.Uint64(body[offset:])
	offset += 8
	if total == math.MaxUint64 {
		return nil, offset, nil
	}
	var data []byte
	for offset+4 <= len(body) {
		n := int(binary.LittleEndian.Uint32(body[offset:]))
		offset += 4
		if n == 0 {
			if total != math.MaxUint64-1 && total != uint64(len(data)) {
				return nil, 0, errors.New("PLP total length mismatch")
			}
			if kind == 0xe7 {
				if len(data)%2 != 0 {
					return nil, 0, errors.New("odd UTF16 PLP length")
				}
				return decodeUTF16LE(data), offset, nil
			}
			if kind == 0xa7 {
				return string(data), offset, nil
			}
			return data, offset, nil
		}
		if n > len(body)-offset {
			break
		}
		data = append(data, body[offset:offset+n]...)
		offset += n
	}
	return nil, 0, errors.New("truncated PLP data")
}
func returnHandle(body []byte) (int32, bool) {
	// sp_prepare returns RETURNSTATUS followed by the handle's RETURNVALUE.
	if len(body) > 5 && body[0] == 0x79 {
		body = body[5:]
	}
	if len(body) < 4 || body[0] != 0xac {
		return 0, false
	}
	offset := 4 + int(body[3])*2 + 7
	if len(body) < offset+7 || body[offset] != 0x26 || body[offset+2] != 4 {
		return 0, false
	}
	var value int32
	_, err := binary.Decode(body[offset+3:], binary.LittleEndian, &value)
	return value, err == nil
}
