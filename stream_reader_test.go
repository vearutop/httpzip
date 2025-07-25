package httpzip_test

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vearutop/httpzip"
)

func TestNewStreamReader(t *testing.T) {
	rw := httptest.NewRecorder()

	h := httpzip.NewHandler("archive")
	h.Streamable = true
	h.OnError = func(err error) {
		log.Println(err)
	}

	c := "hello world"
	crc := crc32.ChecksumIEEE([]byte(c))

	for i := 0; i < 10; i++ {
		if err := h.AddFile(httpzip.FileSource{
			Path:     fmt.Sprintf("file_%d.txt", i),
			Modified: time.Now(),
			Size:     int64(len(c)),
			CRC32:    crc,
			Data: func(w io.Writer) error {
				_, err := w.Write([]byte(c)) // Mimicking actual file source.

				return err
			},
		}); err != nil {
			log.Println(err)
		}
	}

	h.ServeHTTP(rw, nil)

	fmt.Println("Status:", rw.Code)
	fmt.Println("Content-Length:", rw.Header().Get("Content-Length"))

	zr := httpzip.NewStreamReader(rw.Body)

	for {
		e, err := zr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			log.Fatalf("failed to find next file in zip: %s", err)
		}

		log.Println("file path:", e.Name)

		if !e.IsDir() {
			rc, err := e.Open()
			if err != nil {
				log.Fatalf("unable to open zip file entry: %s", err)
			}

			buf := bytes.NewBuffer(nil)

			w, err := io.Copy(buf, rc)
			if err != nil {
				log.Fatalf("unable to stream zip file: %s (%d)", err, w)
			}

			if buf.String() != c {
				log.Fatalf("file contents differs (%q != %q)", buf.String(), c)
			}

			if e.CRC32 != crc {
				log.Fatalf("crc32 differs (%x != %x)", e.CRC32, crc)
			}

			log.Println("file length:", w)

			if err := rc.Close(); err != nil {
				log.Fatalf("failed to close zip file entry: %s", err)
			}
		}
	}
}
