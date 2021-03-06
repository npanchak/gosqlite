// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

/*
#include <sqlite3.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

// BlobReader is an io.ReadCloser adapter for BLOB
// (See http://sqlite.org/c3ref/blob.html)
type BlobReader struct {
	c      *Conn
	bl     *C.sqlite3_blob
	size   int
	offset int
}

// BlobReadWriter is an io.ReadWriteCloser adapter for BLOB
type BlobReadWriter struct {
	BlobReader
}

// ZeroBlobLength is used to reserve space for a BLOB that is later written.
type ZeroBlobLength int

// NewBlobReader opens a BLOB for incremental I/O in read-only mode.
//
// (See http://sqlite.org/c3ref/blob_open.html)
func (c *Conn) NewBlobReader(db, table, column string, row int64) (*BlobReader, error) {
	bl, err := c.blobOpen(db, table, column, row, false)
	if err != nil {
		return nil, err
	}
	return &BlobReader{c, bl, -1, 0}, nil
}

// NewBlobReadWriter opens a BLOB for incremental I/O.
// (See http://sqlite.org/c3ref/blob_open.html)
func (c *Conn) NewBlobReadWriter(db, table, column string, row int64) (*BlobReadWriter, error) {
	bl, err := c.blobOpen(db, table, column, row, true)
	if err != nil {
		return nil, err
	}
	return &BlobReadWriter{BlobReader{c, bl, -1, 0}}, nil
}

func (c *Conn) blobOpen(db, table, column string, row int64, write bool) (*C.sqlite3_blob, error) {
	zDb := C.CString(db)
	defer C.free(unsafe.Pointer(zDb))
	zTable := C.CString(table)
	defer C.free(unsafe.Pointer(zTable))
	zColumn := C.CString(column)
	defer C.free(unsafe.Pointer(zColumn))
	var bl *C.sqlite3_blob
	rv := C.sqlite3_blob_open(c.db, zDb, zTable, zColumn, C.sqlite3_int64(row), btocint(write), &bl)
	if rv != C.SQLITE_OK {
		if bl != nil {
			C.sqlite3_blob_close(bl)
		}
		return nil, c.error(rv, fmt.Sprintf("Conn.blobOpen(db: %q, tbl: %q, col: %q, row: %d)", db, table, column, row))
	}
	if bl == nil {
		return nil, errors.New("sqlite succeeded without returning a blob")
	}
	return bl, nil
}

// Close closes a BLOB handle.
// (See http://sqlite.org/c3ref/blob_close.html)
func (r *BlobReader) Close() error {
	if r == nil {
		return errors.New("nil sqlite blob")
	}
	rv := C.sqlite3_blob_close(r.bl)
	if rv != C.SQLITE_OK {
		return r.c.error(rv, "BlobReader.Close")
	}
	r.bl = nil
	return nil
}

// Read reads data from a BLOB incrementally.
// (See http://sqlite.org/c3ref/blob_read.html)
func (r *BlobReader) Read(v []byte) (int, error) {
	if len(v) == 0 {
		return 0, nil
	}
	size, err := r.Size()
	if err != nil {
		return 0, err
	}
	if r.offset >= size {
		return 0, io.EOF
	}
	if len(v) > (size - r.offset) {
		v = v[0 : size-r.offset]
	}
	p := &v[0]
	n := len(v)
	rv := C.sqlite3_blob_read(r.bl, unsafe.Pointer(p), C.int(n), C.int(r.offset))
	if rv != C.SQLITE_OK {
		return 0, r.c.error(rv, "BlobReader.Read")
	}
	r.offset += n
	return n, nil
}

// Seek sets the offset for the next Read or Write to offset.
func (r *BlobReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0: // SEEK_SET
		r.offset = int(offset)
	case 1: // SEEK_CUR
		r.offset += int(offset)
	case 2: // SEEK_END
		size, err := r.Size()
		if err != nil {
			return 0, err
		}
		r.offset = size + int(offset)
	default:
		return 0, r.c.specificError("Bad seekMode")
	}
	return int64(r.offset), nil
}

// Size returns the size of an opened BLOB.
// (See http://sqlite.org/c3ref/blob_bytes.html)
func (r *BlobReader) Size() (int, error) {
	if r.bl == nil {
		return 0, errors.New("blob reader already closed")
	}
	if r.size < 0 {
		r.size = int(C.sqlite3_blob_bytes(r.bl))
	}
	return r.size, nil
}

// Write writes data into a BLOB incrementally.
// (See http://sqlite.org/c3ref/blob_write.html)
func (w *BlobReadWriter) Write(v []byte) (int, error) {
	if len(v) == 0 {
		return 0, nil
	}
	size, err := w.Size()
	if err != nil {
		return 0, err
	}
	if w.offset >= size {
		return 0, io.EOF
	}
	/* Write must return a non-nil error if it returns n < len(v) */
	if len(v) > (size - w.offset) {
		v = v[0 : size-w.offset]
		err = io.EOF
	}
	p := &v[0]
	n := len(v)
	rv := C.sqlite3_blob_write(w.bl, unsafe.Pointer(p), C.int(n), C.int(w.offset))
	if rv != C.SQLITE_OK {
		return 0, w.c.error(rv, "BlobReadWiter.Write")
	}
	w.offset += n
	return n, err
}

// Reopen moves a BLOB handle to a new row.
// (See http://sqlite.org/c3ref/blob_reopen.html)
func (r *BlobReader) Reopen(rowid int64) error {
	rv := C.sqlite3_blob_reopen(r.bl, C.sqlite3_int64(rowid))
	if rv != C.SQLITE_OK {
		return r.c.error(rv, fmt.Sprintf("BlobReader.Reopen(%d)", rowid))
	}
	r.size = -1
	r.offset = 0
	return nil
}
