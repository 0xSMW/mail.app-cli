package mail

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// Keep the former reader here as a differential oracle for streaming edge
// cases, including corrupt-input offsets after line breaks are discarded.
type legacyMIMENewlineStripper struct{ reader io.Reader }

func (s legacyMIMENewlineStripper) Read(p []byte) (int, error) {
	for {
		n, err := s.reader.Read(p)
		kept := 0
		for _, b := range p[:n] {
			if b != '\r' && b != '\n' {
				p[kept] = b
				kept++
			}
		}
		if kept > 0 || err != nil {
			return kept, err
		}
	}
}

type mimeChunkReader struct {
	data string
	size int
	err  error
}

func (r *mimeChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := min(len(p), r.size, len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err // Exercise readers that return final bytes and an error together.
	}
	return n, nil
}

func TestLocalMIMEBase64ReaderLegacyParity(t *testing.T) {
	readFailure := errors.New("fixture read failed")
	boundary := strings.Repeat("YWFh", 256)
	for _, tc := range []struct {
		name, body string
		want       string
		checkWant  bool
	}{
		{"empty", "", "", true},
		{"CR and LF", "\r\nY\rW\nJj\r\nZA==\r\n", "abcd", true},
		{"newline only chunks", strings.Repeat("\r\n", 80) + "YQ==\n", "a", true},
		{"one padding", "YWI=", "ab", true},
		{"two padding", "YQ==", "a", true},
		{"no padding needed", "YWJj", "abc", true},
		{"malformed alphabet", "Y\r\nW!j", "", false},
		{"space is invalid", "YW Jj", "", false},
		{"bad padding", "Y=Jj", "", false},
		{"extra padding", "YQ===", "", false},
		{"data after padding", "YQ==\r\nYWJj", "", false},
		{"truncated quartet", "YWJ", "", false},
		{"truncated padding", "YQ=", "", false},
		{"one trailing byte", "YWJjY", "", false},
		{"decoder boundary", boundary + "\r\nYQ==", strings.Repeat("aaa", 256) + "a", true},
		{"malformed at boundary", boundary + "\r\n!WJj", "", false},
		{"malformed across boundary", boundary[:1023] + "\r\n!", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunk := range []int{1, 2, 3, 4, 76, 1023, 1024, 4096} {
				for _, terminal := range []error{io.EOF, readFailure} {
					input := func() io.Reader { return &mimeChunkReader{data: tc.body, size: chunk, err: terminal} }
					legacy := base64.NewDecoder(base64.StdEncoding, legacyMIMENewlineStripper{bufio.NewReader(input())})
					current, supported := localMIMETransferReader(input(), " BASE64 ")
					if !supported {
						t.Fatal("base64 must remain supported")
					}
					want, wantErr := io.ReadAll(legacy)
					got, gotErr := io.ReadAll(current)
					if string(got) != string(want) || reflect.TypeOf(gotErr) != reflect.TypeOf(wantErr) || !reflect.DeepEqual(gotErr, wantErr) {
						t.Fatalf("chunk=%d terminal=%v: decoded=%q error=%T(%v); legacy=%q error=%T(%v)", chunk, terminal, got, gotErr, gotErr, want, wantErr, wantErr)
					}
					if tc.checkWant && terminal == io.EOF && (gotErr != nil || string(got) != tc.want) {
						t.Fatalf("chunk=%d: decoded=%q error=%v; want %q", chunk, got, gotErr, tc.want)
					}
					if errors.Is(gotErr, readFailure) != errors.Is(wantErr, readFailure) {
						t.Fatalf("chunk=%d: reader error identity changed", chunk)
					}
				}
			}
		})
	}
}
