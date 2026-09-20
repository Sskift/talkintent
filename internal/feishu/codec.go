package feishu

import (
	"errors"
	"fmt"
)

// Header represents a key-value header in PBBP2 protocol.
type Header struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Frame represents a PBBP2 protocol frame.
type Frame struct {
	SeqID           uint64   `json:"seq_id"`
	LogID           uint64   `json:"log_id"`
	Service         int32    `json:"service"`
	Method          int32    `json:"method"`
	Headers         []Header `json:"headers"`
	PayloadEncoding string   `json:"payload_encoding"`
	PayloadType     string   `json:"payload_type"`
	Payload         []byte   `json:"payload"`
	LogIDNew        string   `json:"log_id_new"`
}

// GetHeader returns the value of the header with the given key, or false if not found.
func (f *Frame) GetHeader(key string) (string, bool) {
	for _, h := range f.Headers {
		if h.Key == key {
			return h.Value, true
		}
	}
	return "", false
}

// HeaderValue returns the value of the header with the given key, or empty string if not found.
func (f *Frame) HeaderValue(key string) string {
	val, _ := f.GetHeader(key)
	return val
}

// SetHeader sets or updates a header key-value pair.
func (f *Frame) SetHeader(key, value string) {
	for i := range f.Headers {
		if f.Headers[i].Key == key {
			f.Headers[i].Value = value
			return
		}
	}
	f.Headers = append(f.Headers, Header{Key: key, Value: value})
}

// Wire type constants for protobuf.
const (
	wireVarint     = 0
	wireFixed64    = 1
	wireBytes      = 2
	wireStartGroup = 3
	wireEndGroup   = 4
	wireFixed32    = 5
)

var (
	errTruncated = errors.New("feishu: truncated protobuf message")
	errBadWire   = errors.New("feishu: invalid protobuf wire type")
	errOverflow  = errors.New("feishu: varint overflow")
)

func encodeVarint(v uint64) []byte {
	var buf [10]byte
	n := 0
	for v >= 0x80 {
		buf[n] = byte(v) | 0x80
		v >>= 7
		n++
	}
	buf[n] = byte(v)
	n++
	return buf[:n]
}

func decodeVarint(data []byte) (uint64, int, error) {
	var val uint64
	var shift uint
	for i, b := range data {
		if i >= 10 {
			return 0, 0, errOverflow
		}
		val |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return val, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errTruncated
}

func (h *Header) marshalAppend(dst []byte) []byte {
	// Field 1: Key (tag = 1<<3 | 2 = 0x0a)
	dst = append(dst, 0x0a)
	dst = append(dst, encodeVarint(uint64(len(h.Key)))...)
	dst = append(dst, h.Key...)

	// Field 2: Value (tag = 2<<3 | 2 = 0x12)
	dst = append(dst, 0x12)
	dst = append(dst, encodeVarint(uint64(len(h.Value)))...)
	dst = append(dst, h.Value...)
	return dst
}

func (h *Header) unmarshal(data []byte) error {
	offset := 0
	for offset < len(data) {
		tagVarint, n, err := decodeVarint(data[offset:])
		if err != nil {
			return err
		}
		offset += n

		fieldNum := tagVarint >> 3
		wireType := tagVarint & 7

		switch fieldNum {
		case 1: // Key
			if wireType != wireBytes {
				return errBadWire
			}
			strLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < strLen {
				return errTruncated
			}
			h.Key = string(data[offset : offset+int(strLen)])
			offset += int(strLen)
		case 2: // Value
			if wireType != wireBytes {
				return errBadWire
			}
			strLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < strLen {
				return errTruncated
			}
			h.Value = string(data[offset : offset+int(strLen)])
			offset += int(strLen)
		default:
			skip, err := skipField(wireType, data[offset:])
			if err != nil {
				return err
			}
			offset += skip
		}
	}
	return nil
}

// Marshal encodes Frame into PBBP2 protobuf wire format.
// Note: Matches the gogo-protobuf serialization from the official Lark SDK:
// - SeqID (tag 1), LogID (tag 2), Service (tag 3), Method (tag 4) are always emitted even when zero.
// - Headers (tag 5) are emitted in slice order.
// - PayloadEncoding (tag 6) is always emitted even when empty ("").
// - PayloadType (tag 7) is always emitted even when empty ("").
// - Payload (tag 8) is only emitted when Payload != nil.
// - LogIDNew (tag 9) is always emitted even when empty ("").
func (f *Frame) Marshal() ([]byte, error) {
	var buf []byte

	// Tag 1: SeqID (1 << 3 | 0 = 0x08)
	buf = append(buf, 0x08)
	buf = append(buf, encodeVarint(f.SeqID)...)

	// Tag 2: LogID (2 << 3 | 0 = 0x10)
	buf = append(buf, 0x10)
	buf = append(buf, encodeVarint(f.LogID)...)

	// Tag 3: Service (3 << 3 | 0 = 0x18)
	buf = append(buf, 0x18)
	buf = append(buf, encodeVarint(uint64(uint32(f.Service)))...)

	// Tag 4: Method (4 << 3 | 0 = 0x20)
	buf = append(buf, 0x20)
	buf = append(buf, encodeVarint(uint64(uint32(f.Method)))...)

	// Tag 5: Headers (5 << 3 | 2 = 0x2a)
	for _, h := range f.Headers {
		buf = append(buf, 0x2a)
		hBytes := h.marshalAppend(nil)
		buf = append(buf, encodeVarint(uint64(len(hBytes)))...)
		buf = append(buf, hBytes...)
	}

	// Tag 6: PayloadEncoding (6 << 3 | 2 = 0x32)
	buf = append(buf, 0x32)
	buf = append(buf, encodeVarint(uint64(len(f.PayloadEncoding)))...)
	buf = append(buf, f.PayloadEncoding...)

	// Tag 7: PayloadType (7 << 3 | 2 = 0x3a)
	buf = append(buf, 0x3a)
	buf = append(buf, encodeVarint(uint64(len(f.PayloadType)))...)
	buf = append(buf, f.PayloadType...)

	// Tag 8: Payload (8 << 3 | 2 = 0x42), only if != nil
	if f.Payload != nil {
		buf = append(buf, 0x42)
		buf = append(buf, encodeVarint(uint64(len(f.Payload)))...)
		buf = append(buf, f.Payload...)
	}

	// Tag 9: LogIDNew (9 << 3 | 2 = 0x4a)
	buf = append(buf, 0x4a)
	buf = append(buf, encodeVarint(uint64(len(f.LogIDNew)))...)
	buf = append(buf, f.LogIDNew...)

	return buf, nil
}

// Unmarshal decodes Frame from PBBP2 protobuf wire format.
func (f *Frame) Unmarshal(data []byte) error {
	*f = Frame{}
	offset := 0

	for offset < len(data) {
		tagVarint, n, err := decodeVarint(data[offset:])
		if err != nil {
			return err
		}
		offset += n

		fieldNum := tagVarint >> 3
		wireType := tagVarint & 7

		switch fieldNum {
		case 1: // SeqID
			if wireType != wireVarint {
				return errBadWire
			}
			val, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			f.SeqID = val
			offset += n
		case 2: // LogID
			if wireType != wireVarint {
				return errBadWire
			}
			val, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			f.LogID = val
			offset += n
		case 3: // Service
			if wireType != wireVarint {
				return errBadWire
			}
			val, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			f.Service = int32(val)
			offset += n
		case 4: // Method
			if wireType != wireVarint {
				return errBadWire
			}
			val, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			f.Method = int32(val)
			offset += n
		case 5: // Headers
			if wireType != wireBytes {
				return errBadWire
			}
			hLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < hLen {
				return errTruncated
			}
			var h Header
			if err := h.unmarshal(data[offset : offset+int(hLen)]); err != nil {
				return err
			}
			f.Headers = append(f.Headers, h)
			offset += int(hLen)
		case 6: // PayloadEncoding
			if wireType != wireBytes {
				return errBadWire
			}
			sLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < sLen {
				return errTruncated
			}
			f.PayloadEncoding = string(data[offset : offset+int(sLen)])
			offset += int(sLen)
		case 7: // PayloadType
			if wireType != wireBytes {
				return errBadWire
			}
			sLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < sLen {
				return errTruncated
			}
			f.PayloadType = string(data[offset : offset+int(sLen)])
			offset += int(sLen)
		case 8: // Payload
			if wireType != wireBytes {
				return errBadWire
			}
			bLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < bLen {
				return errTruncated
			}
			// Copy bytes to avoid referencing input buffer slice directly
			f.Payload = make([]byte, bLen)
			copy(f.Payload, data[offset:offset+int(bLen)])
			offset += int(bLen)
		case 9: // LogIDNew
			if wireType != wireBytes {
				return errBadWire
			}
			sLen, n, err := decodeVarint(data[offset:])
			if err != nil {
				return err
			}
			offset += n
			if uint64(len(data)-offset) < sLen {
				return errTruncated
			}
			f.LogIDNew = string(data[offset : offset+int(sLen)])
			offset += int(sLen)
		default:
			skip, err := skipField(wireType, data[offset:])
			if err != nil {
				return err
			}
			offset += skip
		}
	}
	return nil
}

func skipField(wireType uint64, data []byte) (int, error) {
	switch wireType {
	case wireVarint:
		_, n, err := decodeVarint(data)
		return n, err
	case wireFixed64:
		if len(data) < 8 {
			return 0, errTruncated
		}
		return 8, nil
	case wireBytes:
		l, n, err := decodeVarint(data)
		if err != nil {
			return 0, err
		}
		total := n + int(l)
		if len(data) < total {
			return 0, errTruncated
		}
		return total, nil
	case wireFixed32:
		if len(data) < 4 {
			return 0, errTruncated
		}
		return 4, nil
	default:
		return 0, fmt.Errorf("%w: wire type %d", errBadWire, wireType)
	}
}

// NewPingFrame constructs a ping control frame.
func NewPingFrame(service int32) *Frame {
	return &Frame{
		Service: service,
		Method:  0, // Control
		Headers: []Header{
			{Key: "type", Value: "ping"},
		},
	}
}
