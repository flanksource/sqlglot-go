// Derived from psrth/mori at 7e7223c1a5a3164d7145a608e5220d6991d53426; see LICENSE.mori.
package tds

import (
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"
)

// tdsMsg represents a single TDS wire protocol packet.
// TDS packets have: type(1) + status(1) + length(2 BE) + SPID(2) + packetID(1) + window(1) + payload.
type tdsMsg struct {
	Type     byte   // Packet type
	Status   byte   // Status flags
	SPID     uint16 // Session process ID
	PacketID byte   // Packet ID
	Payload  []byte // Packet payload (after 8-byte header)
	Raw      []byte // Complete raw bytes including header
}

// TDS packet types.
const (
	typeSQLBatch        byte = 0x01
	typeRPC             byte = 0x03
	typeTabularResult   byte = 0x04
	typeAttention       byte = 0x06
	typeTransMgrRequest byte = 0x0E
	typePrelogin        byte = 0x12
	typeLogin7          byte = 0x10
	typeSSPI            byte = 0x11
)

// TDS status flags.
const (
	statusNormal byte = 0x00
	statusEOM    byte = 0x01 // End of message
)

// TDS header size is always 8 bytes.
const tdsHeaderSize = 8

// TDS token types (in tabular result stream).
const (
	tokenError      byte = 0xAA
	tokenDone       byte = 0xFD
	tokenDoneInProc byte = 0xFF
	tokenDoneProc   byte = 0xFE
	tokenEnvChange  byte = 0xE3
	tokenInfo       byte = 0xAB
	tokenLoginAck   byte = 0xAD
)

// readTDSPacket reads a single TDS packet from the reader.
func readTDSPacket(r io.Reader) (*tdsMsg, error) {
	var header [tdsHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}

	pktType := header[0]
	status := header[1]
	pktLen := int(binary.BigEndian.Uint16(header[2:4]))
	spid := binary.BigEndian.Uint16(header[4:6])
	packetID := header[6]

	if pktLen < tdsHeaderSize {
		return nil, fmt.Errorf("invalid TDS packet length: %d", pktLen)
	}

	payloadLen := pktLen - tdsHeaderSize
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("reading TDS payload: %w", err)
		}
	}

	raw := make([]byte, pktLen)
	copy(raw[:tdsHeaderSize], header[:])
	copy(raw[tdsHeaderSize:], payload)

	return &tdsMsg{
		Type:     pktType,
		Status:   status,
		SPID:     spid,
		PacketID: packetID,
		Payload:  payload,
		Raw:      raw,
	}, nil
}

// maxTDSPacketPayload is the maximum payload per TDS packet.
// The default negotiated packet size is 4096 bytes (8 header + up to 4088 payload).
const maxTDSPacketPayload = 4088

// buildTDSPacket constructs a raw TDS packet (single or multi-packet).
// If the payload exceeds the max packet payload size, it's split across
// multiple packets with EOM only on the final packet.
func buildTDSPacket(pktType, status byte, payload []byte) []byte {
	if len(payload) <= maxTDSPacketPayload {
		// Single packet.
		pktLen := tdsHeaderSize + len(payload)
		if pktLen > 65535 {
			panic("TDS packet length exceeds uint16")
		}
		raw := make([]byte, pktLen)
		raw[0] = pktType
		raw[1] = status
		binary.BigEndian.PutUint16(raw[2:4], uint16(pktLen))
		raw[4] = 0 // SPID high
		raw[5] = 0 // SPID low
		raw[6] = 1 // PacketID
		raw[7] = 0 // Window
		copy(raw[tdsHeaderSize:], payload)
		return raw
	}

	// Multi-packet: split payload.
	var result []byte
	packetID := byte(1)
	for len(payload) > 0 {
		chunkSize := len(payload)
		if chunkSize > maxTDSPacketPayload {
			chunkSize = maxTDSPacketPayload
		}

		pktStatus := statusNormal
		if len(payload)-chunkSize == 0 {
			pktStatus = status // Apply EOM only on the last packet.
		}

		pktLen := tdsHeaderSize + chunkSize
		pkt := make([]byte, pktLen)
		pkt[0] = pktType
		pkt[1] = pktStatus
		binary.BigEndian.PutUint16(pkt[2:4], uint16(pktLen))
		pkt[4] = 0
		pkt[5] = 0
		pkt[6] = packetID
		pkt[7] = 0
		copy(pkt[tdsHeaderSize:], payload[:chunkSize])

		result = append(result, pkt...)
		payload = payload[chunkSize:]
		packetID++
	}
	return result
}

// extractSQLFromBatch extracts the SQL text from a SQL_BATCH payload.
// The payload contains an ALL_HEADERS section followed by UTF-16LE SQL text.
func extractSQLFromBatch(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}

	// ALL_HEADERS: first 4 bytes are the total length of headers.
	totalLen := int(binary.LittleEndian.Uint32(payload[0:4]))

	// Validate and skip ALL_HEADERS.
	if totalLen < 4 || totalLen > len(payload) {
		// No ALL_HEADERS or invalid; treat entire payload as SQL.
		return decodeUTF16LE(payload)
	}

	sqlBytes := payload[totalLen:]
	return decodeUTF16LE(sqlBytes)
}

// decodeUTF16LE decodes a UTF-16LE byte slice to a Go string.
func decodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}

	// Ensure even length.
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}

	u16s := make([]uint16, len(b)/2)
	for i := range u16s {
		u16s[i] = binary.LittleEndian.Uint16(b[i*2 : i*2+2])
	}

	runes := utf16.Decode(u16s)
	return string(runes)
}

// encodeUTF16LE encodes a Go string to UTF-16LE bytes.
func encodeUTF16LE(s string) []byte {
	runes := []rune(s)
	u16s := utf16.Encode(runes)
	b := make([]byte, len(u16s)*2)
	for i, v := range u16s {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return b
}
