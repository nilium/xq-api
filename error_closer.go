package main

import "io"

type readCloser interface {
	io.Reader
	Close() // No error returned.
}

type errorCloser struct {
	readCloser
}

func newErrorCloser(rc readCloser) errorCloser {
	return errorCloser{rc}
}

func (e errorCloser) Close() error {
	e.readCloser.Close()
	return nil
}
