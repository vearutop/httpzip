// Package httpzip provides a HTTP handler to serve multiple files in a ZIP stream.
package httpzip

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Handler serves multiple files in uncompressed ZIP.
type Handler struct {
	archiveName string
	tmp         *zip.Writer
	closed      bool
	totalBytes  *countingWriter
	sources     []FileSource

	OnError func(err error)
}

type countingWriter struct {
	written int64
}

func (c *countingWriter) Write(p []byte) (n int, err error) {
	c.written += int64(len(p))

	return len(p), nil
}

// NewHandler creates an instance of Handler.
func NewHandler(archiveName string) *Handler {
	h := &Handler{}
	h.archiveName = archiveName
	h.totalBytes = &countingWriter{}
	h.tmp = zip.NewWriter(h.totalBytes)

	h.OnError = func(err error) {
		println("serve zip: ", err.Error())
	}

	return h
}

// FileSource describes archive entry.
type FileSource struct {
	Path     string
	Modified time.Time
	Size     int64
	CRC32    uint32 // CRC32 checksum of the file content, optional.
	Data     func(w io.Writer) error
}

var tenK = make([]byte, 10000)

// AddFile add a file to the archive.
func (h *Handler) AddFile(fs FileSource) error {
	f, err := h.tmp.CreateRaw(&zip.FileHeader{
		Name:               fs.Path,
		Method:             zip.Store,
		Modified:           fs.Modified,
		CompressedSize64:   uint64(fs.Size),
		UncompressedSize64: uint64(fs.Size),
		CRC32:              fs.CRC32,
	})
	if err != nil {
		return err
	}

	size := int(fs.Size)

	for size > len(tenK) {
		if _, err := f.Write(tenK); err != nil {
			return err
		}

		size -= len(tenK)
	}

	if size > 0 {
		if _, err := f.Write(make([]byte, size)); err != nil {
			return err
		}
	}

	h.sources = append(h.sources, fs)

	return nil
}

func (h *Handler) ServeHTTP(rw http.ResponseWriter, _ *http.Request) {
	if !h.closed {
		if err := h.tmp.Close(); err != nil {
			h.OnError(err)

			return
		}

		h.closed = true
	}

	rw.Header().Set("Content-Type", "application/zip")
	rw.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.zip\"", h.archiveName))
	rw.Header().Set("Content-Length", strconv.Itoa(int(h.totalBytes.written)))

	// Create a new zip archive.
	w := zip.NewWriter(rw)
	defer func() {
		// Make sure to check the error on Close.
		clErr := w.Close()
		if clErr != nil {
			h.OnError(clErr)
		}
	}()

	for _, src := range h.sources {
		f, err := w.CreateRaw(&zip.FileHeader{
			Name:               src.Path,
			Method:             zip.Store,
			Modified:           src.Modified,
			CompressedSize64:   uint64(src.Size),
			UncompressedSize64: uint64(src.Size),
			CRC32:              src.CRC32,
		})
		if err != nil {
			h.OnError(err)

			return
		}

		if err := src.Data(f); err != nil {
			h.OnError(err)

			return
		}
	}
}
