package httpzip_test

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/vearutop/httpzip"
)

func ExampleNewHandler() {
	rw := httptest.NewRecorder()

	h := httpzip.NewHandler("archive")
	h.Streamable = true
	h.OnError = func(err error) {
		log.Println(err)
	}

	c := "hello world"

	for i := 0; i < 10; i++ {
		if err := h.AddFile(httpzip.FileSource{
			Path:     fmt.Sprintf("file_%d.txt", i),
			Modified: time.Now(),
			Size:     int64(len(c)),
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

	// Output:
	// Status: 200
	// Content-Length: 1092
}

func ExampleNewStreamReader() {
	resp, err := http.Get("https://www.example.com/archive.zip")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	zr := httpzip.NewStreamReader(resp.Body)

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

			w, err := io.Copy(io.Discard, rc)
			if err != nil {
				log.Fatalf("unable to stream zip file: %s (%d)", err, w)
			}

			log.Println("file length:", w)

			if err := rc.Close(); err != nil {
				log.Fatalf("failed to close zip file entry: %s", err)
			}
		}
	}
}
