package tds

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	tokenReturnStatus byte = 0x79
	tokenColMetadata  byte = 0x81
	tokenTabName      byte = 0xA4
	tokenColInfo      byte = 0xA5
	tokenOrder        byte = 0xA9
	tokenReturnValue  byte = 0xAC
	tokenFeatureAck   byte = 0xAE
	tokenRow          byte = 0xD1
	tokenNBCRow       byte = 0xD2
	tokenSessionState byte = 0xE4
)

var errTruncatedToken = errors.New("truncated TDS token")

// walkTokens visits every token of a complete tabular response. The data excludes
// the token byte and any token length prefix. Row boundaries come from COLMETADATA,
// so row contents are never mistaken for tokens.
func walkTokens(body []byte, visit func(token byte, data []byte) error) error {
	r := &tokenReader{data: body}
	var columns []typeInfo
	for len(r.data) > 0 {
		token := r.u8()
		rest := r.data
		skipped := 0 // bytes of length prefix not passed to visit
		switch token {
		case tokenReturnStatus:
			r.take(4)
		case tokenDone, tokenDoneProc, tokenDoneInProc:
			r.take(12)
		case tokenError, tokenInfo, tokenEnvChange, tokenLoginAck, tokenOrder, tokenTabName, tokenColInfo:
			skipped = 2
			r.take(int(r.u16()))
		case tokenSessionState:
			skipped = 4
			r.take(int(r.u32()))
		case tokenFeatureAck:
			for r.err == nil && r.u8() != 0xff {
				r.take(int(r.u32()))
			}
		case tokenReturnValue:
			r.returnValue()
		case tokenColMetadata:
			columns = r.columns()
		case tokenRow:
			for _, column := range columns {
				r.value(column)
			}
		case tokenNBCRow:
			nulls := r.take((len(columns) + 7) / 8)
			for i, column := range columns {
				if r.err == nil && nulls[i/8]&(1<<(i%8)) == 0 {
					r.value(column)
				}
			}
		default:
			return fmt.Errorf("unsupported TDS token 0x%02x", token)
		}
		if r.err != nil {
			return fmt.Errorf("token 0x%02x: %w", token, r.err)
		}
		if err := visit(token, rest[skipped:len(rest)-len(r.data)]); err != nil {
			return err
		}
	}
	return nil
}

// typeInfo is the part of TYPE_INFO needed to find where a value ends.
type typeInfo struct {
	kind     byte
	encoding valueEncoding
	fixed    int
}

type valueEncoding int

const (
	fixedValue   valueEncoding = iota
	byteValue                  // one-byte length
	shortValue                 // two-byte length, 0xffff is NULL
	longValue                  // four-byte length, 0 is NULL
	pointerValue               // text pointer, timestamp and four-byte length
	plpValue                   // chunked
)

var fixedSizes = map[byte]int{
	0x1f: 0, 0x30: 1, 0x32: 1, 0x34: 2, 0x38: 4, 0x3a: 4, 0x3b: 4, 0x3c: 8, 0x3d: 8, 0x3e: 8, 0x7a: 4, 0x7f: 8,
}

type tokenReader struct {
	data []byte
	err  error
}

func (r *tokenReader) take(n int) []byte {
	if r.err != nil || n < 0 || n > len(r.data) {
		r.fail(errTruncatedToken)
		return nil
	}
	taken := r.data[:n]
	r.data = r.data[n:]
	return taken
}

func (r *tokenReader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *tokenReader) u8() byte {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *tokenReader) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *tokenReader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (r *tokenReader) byteString()  { r.take(2 * int(r.u8())) }
func (r *tokenReader) shortString() { r.take(2 * int(r.u16())) }

// returnValue reads ordinal, name, status, user type, flags, type and value.
func (r *tokenReader) returnValue() {
	r.take(2)
	r.byteString()
	r.take(1 + 4 + 2)
	r.value(r.typeInfo())
}

func (r *tokenReader) columns() []typeInfo {
	count := r.u16()
	if count == math.MaxUint16 {
		return nil
	}
	var columns []typeInfo
	for range count {
		r.take(4 + 2) // user type, flags
		column := r.typeInfo()
		if column.encoding == pointerValue {
			for range r.u8() { // table name parts
				r.shortString()
			}
		}
		r.byteString()
		if r.err != nil {
			return nil
		}
		columns = append(columns, column)
	}
	return columns
}

func (r *tokenReader) typeInfo() typeInfo {
	info := typeInfo{kind: r.u8()}
	if size, ok := fixedSizes[info.kind]; ok {
		info.fixed = size
		return info
	}
	switch info.kind {
	case 0x28: // date
		info.encoding = byteValue
	case 0x24, 0x26, 0x29, 0x2a, 0x2b, 0x68, 0x6d, 0x6e, 0x6f: // size or scale
		info.encoding = byteValue
		r.take(1)
	case 0x6a, 0x6c: // size, precision, scale
		info.encoding = byteValue
		r.take(3)
	case 0xa5, 0xad, 0xa7, 0xaf, 0xe7, 0xef:
		info.encoding = shortValue
		if r.u16() == math.MaxUint16 {
			info.encoding = plpValue
		}
		if info.kind != 0xa5 && info.kind != 0xad {
			r.take(5) // collation
		}
	case 0x22: // image
		info.encoding = pointerValue
		r.take(4)
	case 0x23, 0x63: // text, ntext
		info.encoding = pointerValue
		r.take(4 + 5)
	case 0x62: // sql_variant
		info.encoding = longValue
		r.take(4)
	case 0xf0: // CLR type: size, database, schema, type, assembly
		info.encoding = plpValue
		r.take(2)
		r.byteString()
		r.byteString()
		r.byteString()
		r.shortString()
	case 0xf1: // xml, optionally with database, schema and collection
		info.encoding = plpValue
		if r.u8() != 0 {
			r.byteString()
			r.byteString()
			r.shortString()
		}
	default:
		r.fail(fmt.Errorf("unsupported column type 0x%02x", info.kind))
	}
	return info
}

func (r *tokenReader) value(info typeInfo) []byte {
	switch info.encoding {
	case fixedValue:
		return r.take(info.fixed)
	case byteValue:
		return r.take(int(r.u8()))
	case shortValue:
		if n := r.u16(); n != math.MaxUint16 {
			return r.take(int(n))
		}
	case longValue:
		return r.take(int(r.u32()))
	case pointerValue:
		if n := r.u8(); n != 0 {
			r.take(int(n) + 8)
			return r.take(int(r.u32()))
		}
	case plpValue:
		r.plp()
	}
	return nil
}

func (r *tokenReader) plp() {
	size := r.take(8)
	if r.err != nil || binary.LittleEndian.Uint64(size) == math.MaxUint64 {
		return
	}
	for r.err == nil {
		n := r.u32()
		if n == 0 {
			return
		}
		r.take(int(n))
	}
}
