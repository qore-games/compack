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
//   - Local file headers carry no file name (unless localNames). Minecraft
//     opens packs with java.util.zip.ZipFile, which takes names from the
//     central directory and only skips the local name's length to find the
//     data, so a pack with 60k files saves its whole path list once more
//     (~2 MB on Minebox's pack). unzip still extracts it (warning that it
//     uses the central names); 7-Zip refuses to open it.
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
	"io"
)

const (
	dummyDOSTime uint16 = 0x0000 // 00:00:00
	dummyDOSDate uint16 = 0x0021 // 1980-01-01 (year 0 + 1980, month 1, day 1)

	// "Version made by" names the host as MS-DOS (0) so that cross-tool
	// extractors do not try to interpret
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

// bodyKey groups entries that may share a body; candidates are then compared
// byte for byte.
type bodyKey struct {
	method     uint16
	crc        uint32
	uncompSize int64
	compSize   int
}

// writeZip writes entries (in order) as a minimal Minecraft-compatible ZIP to
// w. It returns the archive size and the bytes deduplication kept out of it
// (the local headers + bodies of the duplicates).
func writeZip(w io.Writer, entries []entry, localNames bool) (size, dedupSaved int64, err error) {
	bw := bufio.NewWriterSize(w, 1<<20)
	le := binary.LittleEndian

	// Local file headers + bodies. Only the first occurrence of each body is
	// written; duplicates reuse its offset.
	offsets := make([]uint64, len(entries))
	bodies := make(map[bodyKey][]int, len(entries))
	var offset uint64
	for i := range entries {
		e := &entries[i]
		key := bodyKey{e.method, e.crc, e.size, len(e.payload)}
		var name []byte
		if localNames {
			name = []byte(e.rel)
		}
		if j := sameBody(entries, bodies[key], e.payload); j >= 0 {
			offsets[i] = offsets[j]
			dedupSaved += int64(30 + len(name) + len(e.payload))
			continue
		}
		bodies[key] = append(bodies[key], i)
		offsets[i] = offset

		var (
			extra      []byte
			uncompSize = uint64(e.size)
			compSize   = uint64(len(e.payload))
		)
		if uncompSize >= 0xffffffff || compSize >= 0xffffffff {
			extra = zip64ExtraLocal(uncompSize, compSize)
			uncompSize = 0xffffffff
			compSize = 0xffffffff
		}

		hdr := make([]byte, 30+len(name)+len(extra))
		le.PutUint32(hdr[0:4], sigLocalFile)
		le.PutUint16(hdr[4:6], versionNeeded(e.method))
		le.PutUint16(hdr[6:8], utf8Flag(string(name)))
		le.PutUint16(hdr[8:10], e.method)
		le.PutUint16(hdr[10:12], dummyDOSTime)
		le.PutUint16(hdr[12:14], dummyDOSDate)
		le.PutUint32(hdr[14:18], e.crc)
		le.PutUint32(hdr[18:22], uint32(compSize))
		le.PutUint32(hdr[22:26], uint32(uncompSize))
		le.PutUint16(hdr[26:28], uint16(len(name)))
		le.PutUint16(hdr[28:30], uint16(len(extra)))
		copy(hdr[30:], name)
		copy(hdr[30+len(name):], extra)

		if _, err := bw.Write(hdr); err != nil {
			return 0, 0, err
		}
		if _, err := bw.Write(e.payload); err != nil {
			return 0, 0, err
		}
		offset += uint64(len(hdr)) + uint64(len(e.payload))
	}

	// Central directory.
	cdStart := offset
	var cdBuf bytes.Buffer
	for i := range entries {
		e := &entries[i]
		flags := utf8Flag(e.rel)
		vNeeded := versionNeeded(e.method)
		versionMadeBy := (hostMSDOS << 8) | vNeeded

		var (
			extra      []byte
			uncompSize = uint64(e.size)
			compSize   = uint64(len(e.payload))
			localOff   = offsets[i]
		)
		if uncompSize >= 0xffffffff || compSize >= 0xffffffff || localOff >= 0xffffffff {
			extra = zip64ExtraCentral(uncompSize, compSize, localOff)
			uncompSize = min(uncompSize, 0xffffffff)
			compSize = min(compSize, 0xffffffff)
			localOff = min(localOff, 0xffffffff)
		}

		rec := make([]byte, 46+len(e.rel)+len(extra))
		le.PutUint32(rec[0:4], sigCentralDirFile)
		le.PutUint16(rec[4:6], versionMadeBy)
		le.PutUint16(rec[6:8], vNeeded)
		le.PutUint16(rec[8:10], flags)
		le.PutUint16(rec[10:12], e.method)
		le.PutUint16(rec[12:14], dummyDOSTime)
		le.PutUint16(rec[14:16], dummyDOSDate)
		le.PutUint32(rec[16:20], e.crc)
		le.PutUint32(rec[20:24], uint32(compSize))
		le.PutUint32(rec[24:28], uint32(uncompSize))
		le.PutUint16(rec[28:30], uint16(len(e.rel)))
		le.PutUint16(rec[30:32], uint16(len(extra)))
		le.PutUint16(rec[32:34], 0)          // file comment length
		le.PutUint16(rec[34:36], 0)          // disk number start
		le.PutUint16(rec[36:38], 0)          // internal attrs
		le.PutUint32(rec[38:42], fileAttrRO) // external attrs
		le.PutUint32(rec[42:46], uint32(localOff))
		copy(rec[46:], e.rel)
		copy(rec[46+len(e.rel):], extra)
		cdBuf.Write(rec)
	}

	cdBytes := cdBuf.Bytes()
	if _, err := bw.Write(cdBytes); err != nil {
		return 0, 0, err
	}
	cdEnd := cdStart + uint64(len(cdBytes))
	size = int64(cdEnd)

	// Emit the ZIP64 EOCD record + locator only when actually required.
	needZip64 := uint64(len(entries)) >= 0xffff ||
		uint64(len(cdBytes)) >= 0xffffffff ||
		cdEnd >= 0xffffffff

	if needZip64 {
		zip64EOCD := make([]byte, 56)
		le.PutUint32(zip64EOCD[0:4], sigZip64EOCD)
		le.PutUint64(zip64EOCD[4:12], 44) // size of remaining record
		le.PutUint16(zip64EOCD[12:14], (hostMSDOS<<8)|45)
		le.PutUint16(zip64EOCD[14:16], 45) // version needed to extract
		le.PutUint32(zip64EOCD[16:20], 0)  // disk number
		le.PutUint32(zip64EOCD[20:24], 0)  // disk with CD start
		le.PutUint64(zip64EOCD[24:32], uint64(len(entries)))
		le.PutUint64(zip64EOCD[32:40], uint64(len(entries)))
		le.PutUint64(zip64EOCD[40:48], uint64(len(cdBytes)))
		le.PutUint64(zip64EOCD[48:56], cdStart)

		loc := make([]byte, 20)
		le.PutUint32(loc[0:4], sigZip64EOCDLoc)
		le.PutUint32(loc[4:8], 0) // disk with EOCDZ64
		le.PutUint64(loc[8:16], cdEnd)
		le.PutUint32(loc[16:20], 1) // total disks
		if _, err := bw.Write(append(zip64EOCD, loc...)); err != nil {
			return 0, 0, err
		}
		size += 56 + 20
	}

	// End of central directory record (with 0xffffffff sentinels if ZIP64).
	eocd := make([]byte, 22)
	le.PutUint32(eocd[0:4], sigEOCD)
	le.PutUint16(eocd[4:6], 0) // disk number
	le.PutUint16(eocd[6:8], 0) // disk with CD start
	nCD := uint16(len(entries))
	cdSize := uint32(len(cdBytes))
	cdOff := uint32(cdStart)
	if needZip64 {
		nCD, cdSize, cdOff = 0xffff, 0xffffffff, 0xffffffff
	}
	le.PutUint16(eocd[8:10], nCD)
	le.PutUint16(eocd[10:12], nCD)
	le.PutUint32(eocd[12:16], cdSize)
	le.PutUint32(eocd[16:20], cdOff)
	le.PutUint16(eocd[20:22], 0) // archive comment length
	if _, err := bw.Write(eocd); err != nil {
		return 0, 0, err
	}
	size += 22
	return size, dedupSaved, bw.Flush()
}

// sameBody returns the index of the candidate whose body is payload, or -1.
// Compressed bytes are compared: the flate writer is deterministic and
// deflate streams decode to one input only, so equal payloads with the same
// method mean equal files.
func sameBody(entries []entry, candidates []int, payload []byte) int {
	for _, j := range candidates {
		if bytes.Equal(entries[j].payload, payload) {
			return j
		}
	}
	return -1
}

func versionNeeded(method uint16) uint16 {
	if method == zip.Store {
		return 10 // 1.0 for Store
	}
	return 20 // 2.0 for Deflate
}

// utf8Flag general-purpose-bit-flag policy: only set the
// language-encoding flag (bit 11) when the name has non-ASCII bytes.
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
