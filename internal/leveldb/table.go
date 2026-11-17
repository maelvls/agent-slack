package leveldb

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/golang/snappy"
)

const (
	footerSize = 48
	// blockTrailerSize is the compression type byte + CRC32 after a block.
	blockTrailerSize = 5
	// internalKeyTrailer is the (sequence<<8 | type) suffix of table keys.
	internalKeyTrailer = 8
	// maxBlockSize bounds a decompressed block (blocks are ~4 KiB; a
	// single large value makes a block as large as that value).
	maxBlockSize = 256 << 20

	compressionNone   = 0
	compressionSnappy = 1

	typeDeletion = 0
	typeValue    = 1
)

// tableMagic is the little-endian LevelDB table magic 0xdb4775248b80fb57.
var tableMagic = []byte{0x57, 0xfb, 0x80, 0x8b, 0x24, 0x75, 0x47, 0xdb}

var errCorrupt = errors.New("corrupted LevelDB data")

type blockHandle struct{ offset, size uint64 }

// readHandle decodes a block handle (two varints) and returns the rest.
func readHandle(b []byte) (blockHandle, []byte, error) {
	offset, n := binary.Uvarint(b)
	if n <= 0 {
		return blockHandle{}, nil, errCorrupt
	}
	size, m := binary.Uvarint(b[n:])
	if m <= 0 {
		return blockHandle{}, nil, errCorrupt
	}
	return blockHandle{offset, size}, b[n+m:], nil
}

// readBlock returns the decompressed contents of the block at h.
func readBlock(data []byte, h blockHandle) ([]byte, error) {
	total := uint64(len(data))
	if h.offset > total || h.size > total-h.offset || total-h.offset-h.size < blockTrailerSize {
		return nil, errCorrupt
	}
	contents := data[h.offset : h.offset+h.size]
	switch data[h.offset+h.size] {
	case compressionNone:
		return contents, nil
	case compressionSnappy:
		n, err := snappy.DecodedLen(contents)
		if err != nil || n > maxBlockSize {
			return nil, errCorrupt
		}
		return snappy.Decode(nil, contents)
	}
	return nil, errCorrupt // unknown compression (Chromium only uses snappy)
}

// walkBlock calls fn for every key/value of a block, undoing the prefix
// compression of keys. The key slice is only valid during the call.
func walkBlock(block []byte, fn func(key, value []byte)) {
	if len(block) < 4 {
		return
	}
	numRestarts := uint64(binary.LittleEndian.Uint32(block[len(block)-4:]))
	if numRestarts*4+4 > uint64(len(block)) {
		return
	}
	body := block[:uint64(len(block))-4-numRestarts*4]
	var key []byte
	for len(body) > 0 {
		shared, n1 := binary.Uvarint(body)
		if n1 <= 0 {
			return
		}
		nonShared, n2 := binary.Uvarint(body[n1:])
		if n2 <= 0 {
			return
		}
		valueLen, n3 := binary.Uvarint(body[n1+n2:])
		if n3 <= 0 {
			return
		}
		body = body[n1+n2+n3:]
		rest := uint64(len(body))
		if shared > uint64(len(key)) || nonShared > rest || valueLen > rest-nonShared {
			return
		}
		key = append(key[:shared], body[:nonShared]...)
		fn(key, body[nonShared:nonShared+valueLen])
		body = body[nonShared+valueLen:]
	}
}

// parseTable appends the records of a table file (.ldb/.sst) to out.
func parseTable(data []byte, keep func([]byte) bool, out []record) []record {
	if len(data) < footerSize {
		return out
	}
	footer := data[len(data)-footerSize:]
	if !bytes.Equal(footer[footerSize-len(tableMagic):], tableMagic) {
		return out
	}
	// Footer: metaindex handle, index handle, padding, magic.
	_, rest, err := readHandle(footer)
	if err != nil {
		return out
	}
	indexHandle, _, err := readHandle(rest)
	if err != nil {
		return out
	}
	index, err := readBlock(data, indexHandle)
	if err != nil {
		return out
	}
	// Each index entry's value is the handle of one data block.
	walkBlock(index, func(_, handle []byte) {
		h, _, err := readHandle(handle)
		if err != nil {
			return
		}
		block, err := readBlock(data, h)
		if err != nil {
			return // skip a damaged block, keep the others
		}
		walkBlock(block, func(ikey, value []byte) {
			if len(ikey) < internalKeyTrailer {
				return
			}
			userKey := ikey[:len(ikey)-internalKeyTrailer]
			trailer := binary.LittleEndian.Uint64(ikey[len(userKey):])
			kind := byte(trailer)
			if kind != typeValue && kind != typeDeletion {
				return
			}
			if keep != nil && !keep(userKey) {
				return
			}
			r := record{key: bytes.Clone(userKey), seq: trailer >> 8, deleted: kind == typeDeletion}
			if !r.deleted {
				r.value = bytes.Clone(value)
			}
			out = append(out, r)
		})
	})
	return out
}
