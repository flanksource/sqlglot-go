package tds

import "encoding/binary"

// Inspect only token boundaries we can establish; row data is never searched for token bytes.
func responseError(body []byte) string {
	for len(body) > 0 {
		switch body[0] {
		case 0x79:
			if len(body) < 5 {
				return ""
			}
			body = body[5:]
		case tokenDone, tokenDoneProc, tokenDoneInProc:
			if len(body) < 13 {
				return ""
			}
			body = body[13:]
		case tokenError, tokenInfo, tokenEnvChange, tokenLoginAck:
			if len(body) < 3 {
				return ""
			}
			n := int(binary.LittleEndian.Uint16(body[1:]))
			if n > len(body)-3 {
				return ""
			}
			if body[0] == tokenError && n >= 8 {
				chars := int(binary.LittleEndian.Uint16(body[9:]))
				if chars*2 > n-8 {
					return ""
				}
				return decodeUTF16LE(body[11 : 11+chars*2])
			}
			body = body[n+3:]
		default:
			return ""
		}
	}
	return ""
}
