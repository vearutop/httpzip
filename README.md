# httpzip

[![Build Status](https://github.com/vearutop/httpzip/workflows/test-unit/badge.svg)](https://github.com/vearutop/httpzip/actions?query=branch%3Amaster+workflow%3Atest-unit)
[![Coverage Status](https://codecov.io/gh/vearutop/httpzip/branch/master/graph/badge.svg)](https://codecov.io/gh/vearutop/httpzip)
[![GoDevDoc](https://img.shields.io/badge/dev-doc-00ADD8?logo=go)](https://pkg.go.dev/github.com/vearutop/httpzip)
[![Time Tracker](https://wakatime.com/badge/github/vearutop/httpzip.svg)](https://wakatime.com/badge/github/vearutop/httpzip)
![Code lines](https://sloc.xyz/github/vearutop/httpzip/?category=code)
![Comments](https://sloc.xyz/github/vearutop/httpzip/?category=comments)

Serve multiple files in uncompressed ZIP stream with progress status.

```go
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
```