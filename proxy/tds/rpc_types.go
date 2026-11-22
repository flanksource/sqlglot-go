package tds

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// scalarParameter decodes the types whose value follows a one-byte length.
// Values are rendered as text so no precision is lost.
func scalarParameter(body []byte) (any, int, error) {
	kind := body[0]
	info := map[byte]int{0x24: 1, 0x28: 0, 0x29: 1, 0x2a: 1, 0x2b: 1, 0x6a: 3, 0x6c: 3, 0x6e: 1, 0x6f: 1}[kind]
	if len(body) < 2+info {
		return nil, 0, errors.New("truncated RPC parameter type")
	}
	n := int(body[1+info])
	used := 2 + info + n
	if len(body) < used {
		return nil, 0, errors.New("truncated RPC parameter value")
	}
	if n == 0 {
		return nil, used, nil
	}
	value, err := scalarValue(kind, body[1:1+info], body[2+info:used])
	return value, used, err
}

func scalarValue(kind byte, info, data []byte) (any, error) {
	switch {
	case kind == 0x24 && len(data) == 16:
		return fmt.Sprintf("%08x-%04x-%04x-%x-%x", binary.LittleEndian.Uint32(data), binary.LittleEndian.Uint16(data[4:]), binary.LittleEndian.Uint16(data[6:]), data[8:10], data[10:]), nil
	case kind == 0x6a || kind == 0x6c:
		return scaled(littleEndian(data[1:]), data[0] == 0, int(info[2])), nil
	case kind == 0x6e && len(data) == 8:
		var high int32
		if _, err := binary.Decode(data, binary.LittleEndian, &high); err != nil {
			return nil, err
		}
		units := int64(high)<<32 | int64(binary.LittleEndian.Uint32(data[4:]))
		return scaled(new(big.Int).Abs(big.NewInt(units)), units < 0, 4), nil
	case kind == 0x6e && len(data) == 4:
		var units int32
		if _, err := binary.Decode(data, binary.LittleEndian, &units); err != nil {
			return nil, err
		}
		return scaled(new(big.Int).Abs(big.NewInt(int64(units))), units < 0, 4), nil
	case kind == 0x6f && len(data) == 8:
		var days int32
		if _, err := binary.Decode(data, binary.LittleEndian, &days); err != nil {
			return nil, err
		}
		ticks := time.Duration(binary.LittleEndian.Uint32(data[4:]))
		at := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days)).Add(ticks * time.Second / 300)
		return at.Round(time.Millisecond).Format("2006-01-02T15:04:05.000"), nil
	case kind == 0x6f && len(data) == 4:
		at := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(binary.LittleEndian.Uint16(data)))
		return at.Add(time.Duration(binary.LittleEndian.Uint16(data[2:])) * time.Minute).Format("2006-01-02T15:04:05"), nil
	case kind == 0x28 && len(data) == 3:
		return date(data).Format(time.DateOnly), nil
	case kind == 0x29 && len(data) >= 3:
		return timeOfDay(time.Time{}, data, info[0]).Format("15:04:05" + fraction(info[0])), nil
	case kind == 0x2a && len(data) >= 6:
		return timeOfDay(date(data[len(data)-3:]), data[:len(data)-3], info[0]).Format("2006-01-02T15:04:05" + fraction(info[0])), nil
	case kind == 0x2b && len(data) >= 8:
		var minutes int16
		if _, err := binary.Decode(data[len(data)-2:], binary.LittleEndian, &minutes); err != nil {
			return nil, err
		}
		at := timeOfDay(date(data[len(data)-5:len(data)-2]), data[:len(data)-5], info[0])
		return at.In(time.FixedZone("", int(minutes)*60)).Format("2006-01-02T15:04:05" + fraction(info[0]) + "-07:00"), nil
	}
	return nil, errors.New("invalid RPC parameter length or encoding")
}

func littleEndian(data []byte) *big.Int {
	reversed := slices.Clone(data)
	slices.Reverse(reversed)
	return new(big.Int).SetBytes(reversed)
}

func scaled(units *big.Int, negative bool, scale int) string {
	digits := units.String()
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	if scale > 0 {
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func date(data []byte) time.Time {
	days := int(data[0]) | int(data[1])<<8 | int(data[2])<<16
	return time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
}

func timeOfDay(day time.Time, data []byte, scale byte) time.Time {
	nanos := littleEndian(data).Int64()
	for range 9 - min(int(scale), 9) {
		nanos *= 10
	}
	return day.Add(time.Duration(nanos))
}

func fraction(scale byte) string {
	if scale == 0 {
		return ""
	}
	return "." + strings.Repeat("0", min(int(scale), 9))
}
