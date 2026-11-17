package leveldb

import (
	"bytes"
	"encoding/binary"
)

const (
	logBlockSize  = 32768
	logHeaderSize = 7 // crc32(4) + length(2) + type(1)
	// batchHeaderSize is sequence(8) + count(4).
	batchHeaderSize = 12

	logFull   = 1
	logFirst  = 2
	logMiddle = 3
	logLast   = 4
)

// parseLog appends the records of a write-ahead log (.log) to out. The log
// holds the most recent writes, not yet compacted into a table.
func parseLog(data []byte, keep func([]byte) bool, out []record) []record {
	var pending []byte
	inFragment := false
	for off := 0; off < len(data); {
		remaining := logBlockSize - off%logBlockSize
		if remaining < logHeaderSize {
			off += remaining // block trailer padding
			continue
		}
		if off+logHeaderSize > len(data) {
			break
		}
		length := int(binary.LittleEndian.Uint16(data[off+4:]))
		kind := data[off+6]
		if (length == 0 && kind == 0) || logHeaderSize+length > remaining {
			// Preallocated (zeroed) space or a corrupted header: resume
			// at the next block, as LevelDB itself does.
			off += remaining
			inFragment = false
			continue
		}
		if off+logHeaderSize+length > len(data) {
			break // record still being written
		}
		payload := data[off+logHeaderSize : off+logHeaderSize+length]
		off += logHeaderSize + length

		switch kind {
		case logFull:
			inFragment = false
			out = parseBatch(payload, keep, out)
		case logFirst:
			pending = append(pending[:0], payload...)
			inFragment = true
		case logMiddle:
			if inFragment {
				pending = append(pending, payload...)
			}
		case logLast:
			if inFragment {
				pending = append(pending, payload...)
				out = parseBatch(pending, keep, out)
			}
			inFragment = false
		default:
			inFragment = false
		}
	}
	return out
}

// parseBatch decodes a write batch: sequence(8) count(4), then for each
// write: type(1) key [value], with varint-length-prefixed key and value.
// The i-th write of a batch has sequence number sequence+i.
func parseBatch(batch []byte, keep func([]byte) bool, out []record) []record {
	if len(batch) < batchHeaderSize {
		return out
	}
	seq := binary.LittleEndian.Uint64(batch)
	body := batch[batchHeaderSize:]
	for ; len(body) > 0; seq++ {
		kind := body[0]
		if kind != typeValue && kind != typeDeletion {
			return out
		}
		key, rest, ok := readLengthPrefixed(body[1:])
		if !ok {
			return out
		}
		var value []byte
		if kind == typeValue {
			if value, rest, ok = readLengthPrefixed(rest); !ok {
				return out
			}
		}
		body = rest
		if keep != nil && !keep(key) {
			continue
		}
		r := record{key: bytes.Clone(key), seq: seq, deleted: kind == typeDeletion}
		if !r.deleted {
			r.value = bytes.Clone(value)
		}
		out = append(out, r)
	}
	return out
}

func readLengthPrefixed(b []byte) (field, rest []byte, ok bool) {
	n, w := binary.Uvarint(b)
	if w <= 0 || n > uint64(len(b)-w) {
		return nil, nil, false
	}
	return b[w : w+int(n)], b[w+int(n):], true
}
