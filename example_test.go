package httpzip_test

import (
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"time"

	"github.com/vearutop/httpzip"
)

func ExampleNewHandler() {
	rw := httptest.NewRecorder()

	h := httpzip.NewHandler("archive")
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
	// Content-Length: 1432
}
