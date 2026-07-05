package main

// This file implements a minimal, Minecraft-compatible ZIP writer modelled on
// PackSquash's `zip_spec_conformance_level = disregard` mode. It buys size at
// the cost of cross-tool interoperability — exactly the trade-off PackSquash
// makes — via the following tricks:
//
//   - Identical processed files share ONE local file header + body. The central
//     directory has one entry per logical file (so each name is preserved), but
//     every duplicate's central-directory record points at the offset of the
//     first occurrence's local header. Minecraft loads packs through
//     java.util.zip.ZipFile which only walks the central directory, so this is
//     transparent to it.
//   - A dummy DOS time (1980-01-01 00:00:00, PackSquash's DUMMY_SQUASH_TIME) is
//     stored in every local + central record. This avoids emitting the
//     extended-timestamp extra field that archive/zip and friends add, saving
//     ~20 bytes per entry in both records.
//   - CRC, compressed size and uncompressed size are written into the local
//     file header directly, so no data descriptor follows the body (saves 12-16
//     bytes per entry).
//   - No extra fields, no per-file comments, no archive comment: every bit that
//     Minecraft can ignore is omitted.
//   - ZIP64 extended records are emitted only when needed (entry count > 65534
//     or any size/offset crossing the 32-bit boundary), exactly like the
//     standard library does.
//
// The result is byte-for-byte valid for Minecraft's java.util.zip.ZipFile but
// will confuse strict ZIP tools (`unzip`, `jar`, etc.). That is the same
// trade-off PackSquash's `disregard` level makes.

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/flate"
)

const (
	dummyDOSTime uint16 = 0x0000 // 00:00:00
	dummyDOSDate uint16 = 0x0021 // 1980-01-01 (year 0 + 1980, month 1, day 1)

	// as MS-DOS (0) so that cross-tool extractors do not try to interpret
	// the upper 16 bits of the external file attributes as a Unix st_mode.
	// We pair that with the MS-DOS read-only attribute in the external attrs
	// (FILE_ATTRIBUTE_READONLY), so files extracted by Info-ZIP unzip get the user's
	// umask-default perms instead of whatever 0 would yield on a Unix host.
	hostMSDOS  uint16 = 0
	fileAttrRO uint32 = 0x00000001
)

// ZIP record signatures, little-endian "PKxx" magic.
const (
	sigLocalFile      uint32 = 0x04034b50
	sigCentralDirFile uint32 = 0x02014b50
	sigEOCD           uint32 = 0x06054b50
	sigZip64EOCD      uint32 = 0x06064b50
	sigZip64EOCDLoc   uint32 = 0x07064b50
)

// zip64ExtraID is the extra-field header ID reserved for ZIP64 extensions.
const zip64ExtraID uint16 = 0x0001

// sharedEntry is the physical ZIP body for one unique (compression method,
// content) pair. Multiple logical files (one per duplicate) share it via their
// central-directory records pointing at entry.localOffset.
type sharedEntry struct {
	method      uint16 // zip.Store or zip.Deflate
	payload     []byte // compressed bytes for Deflate, raw bytes for Store
	crc         uint32 // CRC-32 of the *uncompressed* payload
	uncompSize  uint64
	compSize    uint64
	localOffset uint64 // assigned when the local header is first written
	written     bool   // has the local header + body been emitted yet
}

// zipDirEntry is one logical file in the resulting ZIP. Several entries may
// point at the same sharedEntry (deduplication).
type zipDirEntry struct {
	rel  string
	body *sharedEntry
}

// writeZipDisregard emits a minimal Minecraft-compatible ZIP to a temp file in
// outZip's directory. It returns the total bytes that deduplication kept out
// of the file (sum of "we would have written this many bytes for the duplicate
// local headers + bodies"), the temp file path to rename into place, and any
// error.
func writeZipDisregard(results []buildResult, outZip string) (tmpName string, dedupSaved int64, err error) {
	f, err := os.CreateTemp(filepath.Dir(outZip), "compack-*.zip.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpName = f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmpName)
	}

	bodies := make(map[uint64]*sharedEntry, len(results))
	entries := make([]zipDirEntry, 0, len(results))

	// First pass: compress (if needed) + dedup key by (method, content hash).
	// klauspost/compress/flate is deterministic, so byte-identical input always
	// compresses to byte-identical output and is safe to share.
	for i := range results {
		r := &results[i]
		key := contentHash(r.method, r.data)

		body, ok := bodies[key]
		if !ok {
			var payload []byte
			if r.method == zip.Deflate {
				var buf bytes.Buffer
				zw, _ := flate.NewWriter(&buf, flate.BestCompression)
				_, _ = zw.Write(r.data)
				_ = zw.Close()
				payload = buf.Bytes()
			} else {
				payload = r.data
			}
			body = &sharedEntry{
				method:     r.method,
				payload:    payload,
				crc:        crc32.ChecksumIEEE(r.data),
				uncompSize: uint64(len(r.data)),
				compSize:   uint64(len(payload)),
			}
			bodies[key] = body
		} else {
			// Sharing this entry's local header + body avoids emitting one
			// local header (30 bytes), the duplicate name, the (optional)
			// ZIP64 extra and the compressed payload bytes. Count the saved
			// bytes so we can report it back to the user.
			dedupSaved += int64(30 + len(r.rel) + int(body.compSize))
		}
		entries = append(entries, zipDirEntry{rel: r.rel, body: body})
	}

	bw := bufio.NewWriterSize(f, 1<<20)

	// Local file headers + compressed bodies. Only the first occurrence of
	// each shared entry writes actual bytes; duplicates reuse the offset.
	var offset uint64
	for i := range entries {
		e := &entries[i]
		b := e.body
		if b.written {
			continue
		}
		b.localOffset = offset
		b.written = true

		flags := utf8Flag(e.rel)
		versionNeeded := uint16(20) // 2.0 for Deflate
		if b.method == zip.Store {
			versionNeeded = 10 // 1.0 for Store
		}

		var (
			extra      []byte
			uncompSize = b.uncompSize
			compSize   = b.compSize
		)
		if uncompSize >= 0xffffffff || compSize >= 0xffffffff {
			extra = zip64ExtraLocal(uncompSize, compSize)
			uncompSize = 0xffffffff
			compSize = 0xffffffff
		}

		hdrLen := 30 + len(e.rel) + len(extra)
		hdr := make([]byte, hdrLen)
		binary.LittleEndian.PutUint32(hdr[0:4], sigLocalFile)
		binary.LittleEndian.PutUint16(hdr[4:6], versionNeeded)
		binary.LittleEndian.PutUint16(hdr[6:8], flags)
		binary.LittleEndian.PutUint16(hdr[8:10], b.method)
		binary.LittleEndian.PutUint16(hdr[10:12], dummyDOSTime)
		binary.LittleEndian.PutUint16(hdr[12:14], dummyDOSDate)
		binary.LittleEndian.PutUint32(hdr[14:18], b.crc)
		binary.LittleEndian.PutUint32(hdr[18:22], uint32(compSize))
		binary.LittleEndian.PutUint32(hdr[22:26], uint32(uncompSize))
		binary.LittleEndian.PutUint16(hdr[26:28], uint16(len(e.rel)))
		binary.LittleEndian.PutUint16(hdr[28:30], uint16(len(extra)))
		copy(hdr[30:], e.rel)
		copy(hdr[30+len(e.rel):], extra)

		if _, err := bw.Write(hdr); err != nil {
			cleanup()
			return "", 0, err
		}
		if _, err := bw.Write(b.payload); err != nil {
			cleanup()
			return "", 0, err
		}
		offset += uint64(hdrLen) + b.compSize
	}

	// Central directory.
	cdStart := offset
	var cdBuf bytes.Buffer
	for i := range entries {
		e := &entries[i]
		b := e.body

		flags := utf8Flag(e.rel)
		versionNeeded := uint16(20)
		if b.method == zip.Store {
			versionNeeded = 10
		}
		versionMadeBy := (hostMSDOS << 8) | versionNeeded

		var (
			extra      []byte
			uncompSize = b.uncompSize
			compSize   = b.compSize
			localOff   = b.localOffset
		)
		if uncompSize >= 0xffffffff || compSize >= 0xffffffff || localOff >= 0xffffffff {
			extra = zip64ExtraCentral(uncompSize, compSize, localOff)
			if uncompSize >= 0xffffffff {
				uncompSize = 0xffffffff
			}
			if compSize >= 0xffffffff {
				compSize = 0xffffffff
			}
			if localOff >= 0xffffffff {
				localOff = 0xffffffff
			}
		}

		recLen := 46 + len(e.rel) + len(extra)
		rec := make([]byte, recLen)
		binary.LittleEndian.PutUint32(rec[0:4], sigCentralDirFile)
		binary.LittleEndian.PutUint16(rec[4:6], versionMadeBy)
		binary.LittleEndian.PutUint16(rec[6:8], versionNeeded)
		binary.LittleEndian.PutUint16(rec[8:10], flags)
		binary.LittleEndian.PutUint16(rec[10:12], b.method)
		binary.LittleEndian.PutUint16(rec[12:14], dummyDOSTime)
		binary.LittleEndian.PutUint16(rec[14:16], dummyDOSDate)
		binary.LittleEndian.PutUint32(rec[16:20], b.crc)
		binary.LittleEndian.PutUint32(rec[20:24], uint32(compSize))
		binary.LittleEndian.PutUint32(rec[24:28], uint32(uncompSize))
		binary.LittleEndian.PutUint16(rec[28:30], uint16(len(e.rel)))
		binary.LittleEndian.PutUint16(rec[30:32], uint16(len(extra)))
		binary.LittleEndian.PutUint16(rec[32:34], 0)          // file comment length
		binary.LittleEndian.PutUint16(rec[34:36], 0)          // disk number start
		binary.LittleEndian.PutUint16(rec[36:38], 0)          // internal attrs
		binary.LittleEndian.PutUint32(rec[38:42], fileAttrRO) // external attrs
		binary.LittleEndian.PutUint32(rec[42:46], uint32(localOff))
		copy(rec[46:], e.rel)
		copy(rec[46+len(e.rel):], extra)

		if _, err := cdBuf.Write(rec); err != nil {
			cleanup()
			return "", 0, err
		}
	}

	cdBytes := cdBuf.Bytes()
	if _, err := bw.Write(cdBytes); err != nil {
		cleanup()
		return "", 0, err
	}
	cdEnd := cdStart + uint64(len(cdBytes))

	// Emit the ZIP64 EOCD record + locator only when actually required.
	needZip64 := uint64(len(entries)) >= 0xffff ||
		uint64(len(cdBytes)) >= 0xffffffff ||
		cdEnd >= 0xffffffff

	if needZip64 {
		zip64EOCD := make([]byte, 56)
		binary.LittleEndian.PutUint32(zip64EOCD[0:4], sigZip64EOCD)
		binary.LittleEndian.PutUint64(zip64EOCD[4:12], 44) // size of remaining record
		binary.LittleEndian.PutUint16(zip64EOCD[12:14], (hostMSDOS<<8)|45)
		binary.LittleEndian.PutUint16(zip64EOCD[14:16], 45) // version needed to extract
		binary.LittleEndian.PutUint32(zip64EOCD[16:20], 0)  // disk number
		binary.LittleEndian.PutUint32(zip64EOCD[20:24], 0)  // disk with CD start
		binary.LittleEndian.PutUint64(zip64EOCD[24:32], uint64(len(entries)))
		binary.LittleEndian.PutUint64(zip64EOCD[32:40], uint64(len(entries)))
		binary.LittleEndian.PutUint64(zip64EOCD[40:48], uint64(len(cdBytes)))
		binary.LittleEndian.PutUint64(zip64EOCD[48:56], cdStart)
		if _, err := bw.Write(zip64EOCD); err != nil {
			cleanup()
			return "", 0, err
		}

		loc := make([]byte, 20)
		binary.LittleEndian.PutUint32(loc[0:4], sigZip64EOCDLoc)
		binary.LittleEndian.PutUint32(loc[4:8], 0) // disk with EOCDZ64
		binary.LittleEndian.PutUint64(loc[8:16], cdEnd)
		binary.LittleEndian.PutUint32(loc[16:20], 1) // total disks
		if _, err := bw.Write(loc); err != nil {
			cleanup()
			return "", 0, err
		}
	}

	// End of central directory record (with 0xffffffff sentinels if ZIP64).
	eocd := make([]byte, 22)
	binary.LittleEndian.PutUint32(eocd[0:4], sigEOCD)
	binary.LittleEndian.PutUint16(eocd[4:6], 0) // disk number
	binary.LittleEndian.PutUint16(eocd[6:8], 0) // disk with CD start
	nCD := uint16(len(entries))
	if needZip64 {
		nCD = 0xffff
	}
	binary.LittleEndian.PutUint16(eocd[8:10], nCD)
	binary.LittleEndian.PutUint16(eocd[10:12], nCD)
	cdSize := uint32(len(cdBytes))
	if needZip64 {
		cdSize = 0xffffffff
	}
	binary.LittleEndian.PutUint32(eocd[12:16], cdSize)
	cdOff := uint32(cdStart)
	if needZip64 {
		cdOff = 0xffffffff
	}
	binary.LittleEndian.PutUint32(eocd[16:20], cdOff)
	binary.LittleEndian.PutUint16(eocd[20:22], 0) // archive comment length
	if _, err := bw.Write(eocd); err != nil {
		cleanup()
		return "", 0, err
	}

	if err := bw.Flush(); err != nil {
		cleanup()
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, err
	}
	return tmpName, dedupSaved, nil
}

// contentHash keys deduplication by (method, content). FNV-1a 64-bit has a
// collision probability of order n^2 / 2^64; for ~10^6 files that is well
// below the bit-error rate of commodity storage, so byte-equality is not
// re-checked after the hash lookup. Compressed bytes for a Deflate entry are
// produced by klauspost/compress/flate which is deterministic, so matching the
// (method, uncompressed content) pair is sufficient to share compressed bodies
// safely.
func contentHash(method uint16, data []byte) uint64 {
	h := fnv.New64a()
	var m [2]byte
	binary.LittleEndian.PutUint16(m[:], method)
	h.Write(m[:])
	h.Write(data)
	return h.Sum64()
}

// utf8Flag general-purpose-bit-flag policy: only set the
// language-encoding flag (bit 11) when the name has non-ASCII bytes. ASCII
// names leave it unset, which improves compressibility for duplicated headers.
func utf8Flag(name string) uint16 {
	for i := 0; i < len(name); i++ {
		if name[i] > 0x7f {
			return 0x0800
		}
	}
	return 0
}

// zip64ExtraLocal is the ZIP64 extended-information extra field for local file
// headers: it carries the real (64-bit) uncompressed + compressed sizes.
func zip64ExtraLocal(uncompSize, compSize uint64) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint16(b[0:2], zip64ExtraID)
	binary.LittleEndian.PutUint16(b[2:4], 16)
	binary.LittleEndian.PutUint64(b[4:12], uncompSize)
	binary.LittleEndian.PutUint64(b[12:20], compSize)
	return b
}

// zip64ExtraCentral is the ZIP64 extended-information extra field for central
// directory entries: it carries the real (64-bit) uncompressed size,
// compressed size, and local-header offset.
func zip64ExtraCentral(uncompSize, compSize, localOff uint64) []byte {
	b := make([]byte, 28)
	binary.LittleEndian.PutUint16(b[0:2], zip64ExtraID)
	binary.LittleEndian.PutUint16(b[2:4], 24)
	binary.LittleEndian.PutUint64(b[4:12], uncompSize)
	binary.LittleEndian.PutUint64(b[12:20], compSize)
	binary.LittleEndian.PutUint64(b[20:28], localOff)
	return b
}

// pin unused imports during partial edits.
var (
	_ io.Writer = (*bufio.Writer)(nil)
)
