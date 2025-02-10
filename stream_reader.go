// StreamReader uses code from Go archive/zip with modifications.
// Please find original license below.

/*
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/

package httpzip

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"time"
)

const (
	headerIdentifierLen = 4
	fileHeaderLen       = 26

	dataDescriptorLen        = 16 // four uint32: descriptor signature, crc32, compressed size, size
	fileHeaderSignature      = 0x04034b50
	directoryHeaderSignature = 0x02014b50
	directoryEndSignature    = 0x06054b50
	dataDescriptorSignature  = 0x08074b50
)

// Extra header IDs.
// See http://mdfs.net/Docs/Comp/Archiving/Zip/ExtraField.
const (
	Zip64ExtraID       = 0x0001 // Zip64 extended information.
	NtfsExtraID        = 0x000a // NTFS.
	UnixExtraID        = 0x000d // UNIX.
	ExtTimeExtraID     = 0x5455 // Extended timestamp.
	InfoZipUnixExtraID = 0x5855 // Info-ZIP Unix extension.
)

// Entry represents a file or directory in ZIP archive.
type Entry struct {
	zip.FileHeader
	r                          io.Reader
	lr                         io.Reader // LimitReader
	zip64                      bool
	hasReadNum                 uint64
	hasDataDescriptorSignature bool
	eof                        bool
}

func (e *Entry) hasDataDescriptor() bool {
	return e.Flags&8 != 0
}

// IsDir returns true for directories.
func (e *Entry) IsDir() bool {
	return len(e.Name) > 0 && e.Name[len(e.Name)-1] == '/'
}

// Open opens entry contents for reading.
func (e *Entry) Open() (io.ReadCloser, error) {
	if e.eof {
		return nil, errors.New("this file has read to end")
	}

	decomp := decompressor(e.Method)
	if decomp == nil {
		return nil, zip.ErrAlgorithm
	}

	rc := decomp(e.lr)

	return &checksumReader{
		rc:    rc,
		hash:  crc32.NewIEEE(),
		entry: e,
	}, nil
}

// StreamReader can read ZIP contents from a io.Reader.
type StreamReader struct {
	r            io.Reader
	localFileEnd bool
	curEntry     *Entry
}

// NewStreamReader returns streaming ZIP reader.
func NewStreamReader(r io.Reader) *StreamReader {
	return &StreamReader{
		r: r,
	}
}

func (z *StreamReader) readEntry() (*Entry, error) {
	buf := make([]byte, fileHeaderLen)
	if _, err := io.ReadFull(z.r, buf); err != nil {
		return nil, fmt.Errorf("unable to read local file header: %w", err)
	}

	lr := readBuf(buf)

	readerVersion := lr.uint16()
	flags := lr.uint16()
	method := lr.uint16()
	modifiedTime := lr.uint16()
	modifiedDate := lr.uint16()
	crc32Sum := lr.uint32()
	compressedSize := lr.uint32()
	uncompressedSize := lr.uint32()
	filenameLen := int(lr.uint16())
	extraAreaLen := int(lr.uint16())

	entry := &Entry{
		FileHeader: zip.FileHeader{
			ReaderVersion:      readerVersion,
			Flags:              flags,
			Method:             method,
			ModifiedTime:       modifiedTime,
			ModifiedDate:       modifiedDate,
			Modified:           msDosTimeToTime(modifiedDate, modifiedTime),
			CRC32:              crc32Sum,
			CompressedSize:     compressedSize,
			UncompressedSize:   uncompressedSize,
			CompressedSize64:   uint64(compressedSize),
			UncompressedSize64: uint64(uncompressedSize),
		},
		r:          z.r,
		hasReadNum: 0,
		eof:        false,
	}

	nameAndExtraBuf := make([]byte, filenameLen+extraAreaLen)
	if _, err := io.ReadFull(z.r, nameAndExtraBuf); err != nil {
		return nil, fmt.Errorf("unable to read entry name and extra area: %w", err)
	}

	entry.Name = string(nameAndExtraBuf[:filenameLen])
	entry.Extra = nameAndExtraBuf[filenameLen:]
	entry.NonUTF8 = flags&0x800 == 0

	if flags&1 == 1 {
		return nil, errors.New("encrypted ZIP entry not supported")
	}

	needCSize := entry.CompressedSize == ^uint32(0)
	needUSize := entry.UncompressedSize == ^uint32(0)

	ler := readBuf(entry.Extra)
	modified := time.Time{}
parseExtras:
	for len(ler) >= 4 { // need at least tag and size
		fieldTag := ler.uint16()
		fieldSize := int(ler.uint16())
		if len(ler) < fieldSize {
			break
		}
		fieldBuf := ler.sub(fieldSize)

		switch fieldTag {
		case Zip64ExtraID:
			entry.zip64 = true

			// update directory values from the zip64 extra block.
			// They should only be consulted if the sizes read earlier
			// are maxed out.
			// See golang.org/issue/13367.
			if needUSize {
				needUSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				entry.UncompressedSize64 = fieldBuf.uint64()
			}
			if needCSize {
				needCSize = false
				if len(fieldBuf) < 8 {
					return nil, zip.ErrFormat
				}
				entry.CompressedSize64 = fieldBuf.uint64()
			}
		case NtfsExtraID:
			if len(fieldBuf) < 4 {
				continue parseExtras
			}
			fieldBuf.uint32()        // reserved (ignored)
			for len(fieldBuf) >= 4 { // need at least tag and size
				attrTag := fieldBuf.uint16()
				attrSize := int(fieldBuf.uint16())
				if len(fieldBuf) < attrSize {
					continue parseExtras
				}
				attrBuf := fieldBuf.sub(attrSize)
				if attrTag != 1 || attrSize != 24 {
					continue // Ignore irrelevant attributes
				}

				const ticksPerSecond = 1e7    // Windows timestamp resolution
				ts := int64(attrBuf.uint64()) // ModTime since Windows epoch
				secs := ts / ticksPerSecond
				nsecs := (1e9 / ticksPerSecond) * ts % ticksPerSecond
				epoch := time.Date(1601, time.January, 1, 0, 0, 0, 0, time.UTC)
				modified = time.Unix(epoch.Unix()+secs, nsecs)
			}
		case UnixExtraID, InfoZipUnixExtraID:
			if len(fieldBuf) < 8 {
				continue parseExtras
			}
			fieldBuf.uint32()              // AcTime (ignored)
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		case ExtTimeExtraID:
			if len(fieldBuf) < 5 || fieldBuf.uint8()&1 == 0 {
				continue parseExtras
			}
			ts := int64(fieldBuf.uint32()) // ModTime since Unix epoch
			modified = time.Unix(ts, 0)
		}
	}

	if !modified.IsZero() {
		entry.Modified = modified.UTC()

		// If legacy MS-DOS timestamps are set, we can use the delta between
		// the legacy and extended versions to estimate timezone offset.
		//
		// A non-UTC timezone is always used (even if offset is zero).
		// Thus, FileHeader.Modified.Location() == time.UTC is useful for
		// determining whether extended timestamps are present.
		// This is necessary for users that need to do additional time
		// calculations when dealing with legacy ZIP formats.
		if entry.ModifiedTime != 0 || entry.ModifiedDate != 0 {
			entry.Modified = modified.In(timeZone(entry.Modified.Sub(modified)))
		}
	}

	if needCSize {
		return nil, zip.ErrFormat
	}

	entry.lr = io.LimitReader(z.r, int64(entry.CompressedSize64))

	return entry, nil
}

// Next returns the next Entry.
func (z *StreamReader) Next() (*Entry, error) {
	if z.localFileEnd {
		return nil, io.EOF
	}

	if z.curEntry != nil && !z.curEntry.eof {
		if z.curEntry.hasReadNum <= z.curEntry.UncompressedSize64 {
			if _, err := io.Copy(io.Discard, z.curEntry.lr); err != nil {
				return nil, fmt.Errorf("read previous file data fail: %w", err)
			}

			if z.curEntry.hasDataDescriptor() {
				if err := readDataDescriptor(z.r, z.curEntry); err != nil {
					return nil, fmt.Errorf("read previous entry's data descriptor fail: %w", err)
				}
			}
		} else {
			if !z.curEntry.hasDataDescriptor() {
				return nil, errors.New("parse error, read position exceed entry")
			}

			readDataLen := z.curEntry.hasReadNum - z.curEntry.UncompressedSize64
			if readDataLen > dataDescriptorLen {
				return nil, errors.New("parse error, read position exceed entry")
			} else if readDataLen > dataDescriptorLen-4 {
				if z.curEntry.hasDataDescriptorSignature {
					if _, err := io.Copy(io.Discard, io.LimitReader(z.r, int64(dataDescriptorLen-readDataLen))); err != nil {
						return nil, fmt.Errorf("read previous entry's data descriptor fail: %w", err)
					}
				} else {
					return nil, errors.New("parse error, read position exceed entry")
				}
			} else {
				buf := make([]byte, dataDescriptorLen-readDataLen)
				if _, err := io.ReadFull(z.r, buf); err != nil {
					return nil, fmt.Errorf("read previous entry's data descriptor fail: %w", err)
				}

				buf = buf[len(buf)-4:]

				headerID := binary.LittleEndian.Uint32(buf)

				// read to next record head
				if headerID == fileHeaderSignature ||
					headerID == directoryHeaderSignature ||
					headerID == directoryEndSignature {
					z.r = io.MultiReader(bytes.NewReader(buf), z.r)
				}
			}
		}

		z.curEntry.eof = true
	}

	headerIDBuf := make([]byte, headerIdentifierLen)

	if _, err := io.ReadFull(z.r, headerIDBuf); err != nil {
		return nil, fmt.Errorf("unable to read header identifier: %w", err)
	}

	headerID := binary.LittleEndian.Uint32(headerIDBuf)

	if headerID != fileHeaderSignature {
		if headerID == directoryHeaderSignature || headerID == directoryEndSignature {
			z.localFileEnd = true

			return nil, io.EOF
		}

		return nil, zip.ErrFormat
	}

	entry, err := z.readEntry()
	if err != nil {
		return nil, fmt.Errorf("unable to read zip file header: %w", err)
	}

	z.curEntry = entry

	return entry, nil
}

func decompressor(method uint16) zip.Decompressor {
	switch method {
	case zip.Deflate:
		return flate.NewReader
	case zip.Store:
		return io.NopCloser
	default:
		return nil
	}
}

func readDataDescriptor(r io.Reader, entry *Entry) error {
	var buf [dataDescriptorLen]byte
	// The spec says: "Although not originally assigned a
	// signature, the value 0x08074b50 has commonly been adopted
	// as a signature value for the data descriptor record.
	// Implementers should be aware that ZIP files may be
	// encountered with or without this signature marking data
	// descriptors and should account for either case when reading
	// ZIP files to ensure compatibility."
	//
	// dataDescriptorLen includes the size of the signature but
	// first read just those 4 bytes to see if it exists.
	n, err := io.ReadFull(r, buf[:4])
	entry.hasReadNum += uint64(n)

	if err != nil {
		return err
	}

	off := 0
	maybeSig := readBuf(buf[:4])

	if maybeSig.uint32() != dataDescriptorSignature {
		// No data descriptor signature. Keep these four
		// bytes.
		off += 4
	} else {
		entry.hasDataDescriptorSignature = true
	}

	n, err = io.ReadFull(r, buf[off:12])
	entry.hasReadNum += uint64(n)

	if err != nil {
		return err
	}

	entry.eof = true

	b := readBuf(buf[:12])

	if entry.CRC32 != 0 && b.uint32() != entry.CRC32 {
		return zip.ErrChecksum
	}

	// The two sizes that follow here can be either 32 bits or 64 bits
	// but the spec is not very clear on this and different
	// interpretations has been made causing incompatibilities. We
	// already have the sizes from the central directory so we can
	// just ignore these.

	return nil
}

type checksumReader struct {
	rc    io.ReadCloser
	hash  hash.Hash32
	nread uint64 // number of bytes read so far
	entry *Entry
	err   error // sticky error
}

func (r *checksumReader) Read(b []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err := r.rc.Read(b)
	r.hash.Write(b[:n])
	r.nread += uint64(n)
	r.entry.hasReadNum += uint64(n)

	if err == nil {
		return n, nil
	}

	if err == io.EOF {
		if r.nread != r.entry.UncompressedSize64 {
			return 0, io.ErrUnexpectedEOF
		}

		if r.entry.hasDataDescriptor() {
			if err1 := readDataDescriptor(r.entry.r, r.entry); err1 != nil {
				if err1 == io.EOF {
					err = io.ErrUnexpectedEOF
				} else {
					err = err1
				}
			} else if r.hash.Sum32() != r.entry.CRC32 {
				err = zip.ErrChecksum
			}
		} else {
			// If there's not a data descriptor, we still compare
			// the CRC32 of what we've read against the file header
			// or TOC's CRC32, if it seems like it was set.
			r.entry.eof = true
			if r.entry.CRC32 != 0 && r.hash.Sum32() != r.entry.CRC32 {
				err = zip.ErrChecksum
			}
		}
	}

	r.err = err

	return n, err
}

func (r *checksumReader) Close() error { return r.rc.Close() }

func msDosTimeToTime(dosDate, dosTime uint16) time.Time {
	return time.Date(
		// date bits 0-4: day of month; 5-8: month; 9-15: years since 1980
		int(dosDate>>9+1980),
		time.Month(dosDate>>5&0xf),
		int(dosDate&0x1f),

		// time bits 0-4: second/2; 5-10: minute; 11-15: hour
		int(dosTime>>11),
		int(dosTime>>5&0x3f),
		int(dosTime&0x1f*2),
		0, // nanoseconds

		time.UTC,
	)
}

// timeZone returns a *time.Location based on the provided offset.
// If the offset is non-sensible, then this uses an offset of zero.
func timeZone(offset time.Duration) *time.Location {
	const (
		minOffset   = -12 * time.Hour  // E.g., Baker island at -12:00
		maxOffset   = +14 * time.Hour  // E.g., Line island at +14:00
		offsetAlias = 15 * time.Minute // E.g., Nepal at +5:45
	)

	offset = offset.Round(offsetAlias)
	if offset < minOffset || offset > maxOffset {
		return time.UTC
	}

	return time.FixedZone("", int(offset/time.Second))
}

type readBuf []byte

func (b *readBuf) uint8() uint8 {
	v := (*b)[0]
	*b = (*b)[1:]

	return v
}

func (b *readBuf) uint16() uint16 {
	v := binary.LittleEndian.Uint16(*b)
	*b = (*b)[2:]

	return v
}

func (b *readBuf) uint32() uint32 {
	v := binary.LittleEndian.Uint32(*b)
	*b = (*b)[4:]

	return v
}

func (b *readBuf) uint64() uint64 {
	v := binary.LittleEndian.Uint64(*b)
	*b = (*b)[8:]

	return v
}

func (b *readBuf) sub(n int) readBuf {
	b2 := (*b)[:n]
	*b = (*b)[n:]

	return b2
}
