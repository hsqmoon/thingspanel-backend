package utils

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type testReadCloser struct {
	io.Reader
	closeErr error
	closed   *bool
}

func (r testReadCloser) Close() error {
	if r.closed != nil {
		*r.closed = true
	}
	return r.closeErr
}

func TestFileSignPropagatesReadError(t *testing.T) {
	readErr := errors.New("read failed")
	closed := false
	_, err := fileSign(testReadCloser{Reader: errorReader{err: readErr}, closed: &closed}, "SHA256")
	if !errors.Is(err, readErr) {
		t.Fatalf("expected read error, got %v", err)
	}
	if !closed {
		t.Fatal("reader was not closed after copy failure")
	}
}

func TestFileSignPropagatesCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	signature, err := fileSign(testReadCloser{Reader: strings.NewReader("firmware"), closeErr: closeErr}, "SHA256")
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error, got %v", err)
	}
	if signature != "" {
		t.Fatalf("signature must be empty when close fails, got %q", signature)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
