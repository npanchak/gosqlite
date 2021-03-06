// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log"
	"os"
	"reflect"
	"time"
	"unsafe"
)

func init() {
	sql.Register("sqlite3", &impl{})
	if os.Getenv("SQLITE_LOG") != "" {
		ConfigLog(func(d interface{}, err error, msg string) {
			log.Printf("%s: %s, %s\n", d, err, msg)
		}, "SQLITE")
	}
	ConfigMemStatus(false)
}

// impl is an adapter to database/sql/driver
type impl struct {
}
type conn struct {
	c *Conn
}
type stmt struct {
	s            *Stmt
	rowsRef      bool // true if there is a rowsImpl associated to this statement that has not been closed.
	pendingClose bool
}
type rowsImpl struct {
	s           *stmt
	columnNames []string // cache
}

// Open opens a new database connection.
// ":memory:" for memory db,
// "" for temp file db
func (d *impl) Open(name string) (driver.Conn, error) {
	// OpenNoMutex == multi-thread mode (http://sqlite.org/compile.html#threadsafe and http://sqlite.org/threadsafe.html)
	c, err := Open(name, OpenUri, OpenNoMutex, OpenReadWrite, OpenCreate)
	if err != nil {
		return nil, err
	}
	c.BusyTimeout(time.Duration(10) * time.Second)
	return &conn{c}, nil
}

// PRAGMA schema_version may be used to detect when the database schema is altered

func (c *conn) Exec(query string, args []driver.Value) (driver.Result, error) {
	// https://code.google.com/p/go-wiki/wiki/cgo#Turning_C_arrays_into_Go_slices
	var iargs []interface{}
	if len(args) > 0 {
		h := (*reflect.SliceHeader)(unsafe.Pointer(&iargs))
		h.Data = uintptr(unsafe.Pointer(&args[0]))
		h.Len = len(args)
		h.Cap = cap(args)
	}
	if err := c.c.Exec(query, iargs...); err != nil {
		return nil, err
	}
	return c, nil // FIXME RowAffected/noRows
}

// TODO How to know that the last Stmt has done an INSERT? An authorizer?
func (c *conn) LastInsertId() (int64, error) {
	return c.c.LastInsertRowid(), nil
}

// TODO How to know that the last Stmt has done a DELETE/INSERT/UPDATE? An authorizer?
func (c *conn) RowsAffected() (int64, error) {
	return int64(c.c.Changes()), nil
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.c.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &stmt{s: s}, nil
}

func (c *conn) Close() error {
	return c.c.Close()
}

func (c *conn) Begin() (driver.Tx, error) {
	if err := c.c.Begin(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *conn) Commit() error {
	return c.c.Commit()
}
func (c *conn) Rollback() error {
	return c.c.Rollback()
}

func (s *stmt) Close() error {
	if s.rowsRef { // Currently, it never happens because the sql.Stmt doesn't call driver.Stmt in this case
		s.pendingClose = true
		return nil
	}
	return s.s.Finalize()
}

func (s *stmt) NumInput() int {
	return s.s.BindParameterCount()
}

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.bind(args); err != nil {
		return nil, err
	}
	if err := s.s.exec(); err != nil {
		return nil, err
	}
	return s, nil // FIXME RowAffected/noRows
}

// TODO How to know that this Stmt has done an INSERT? An authorizer?
func (s *stmt) LastInsertId() (int64, error) {
	return s.s.c.LastInsertRowid(), nil
}

// TODO How to know that this Stmt has done a DELETE/INSERT/UPDATE? An authorizer?
func (s *stmt) RowsAffected() (int64, error) {
	return int64(s.s.c.Changes()), nil
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.rowsRef {
		return nil, errors.New("Previously returned Rows still not closed")
	}
	if err := s.bind(args); err != nil {
		return nil, err
	}
	s.rowsRef = true
	return &rowsImpl{s, nil}, nil
}

func (s *stmt) bind(args []driver.Value) error {
	for i, v := range args {
		if err := s.s.BindByIndex(i+1, v); err != nil {
			return err
		}
	}
	return nil
}

func (r *rowsImpl) Columns() []string {
	if r.columnNames == nil {
		r.columnNames = r.s.s.ColumnNames()
	}
	return r.columnNames
}

func (r *rowsImpl) Next(dest []driver.Value) error {
	ok, err := r.s.s.Next()
	if err != nil {
		return err
	}
	if !ok {
		return io.EOF
	}
	for i := range dest {
		dest[i], _ = r.s.s.ScanValue(i, true)
		/*if !driver.IsScanValue(dest[i]) {
			panic("Invalid type returned by ScanValue")
		}*/
	}
	return nil
}

func (r *rowsImpl) Close() error {
	r.s.rowsRef = false
	if r.s.pendingClose {
		return r.s.Close()
	}
	return r.s.s.Reset()
}
