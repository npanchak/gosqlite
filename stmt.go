// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

/*
#include <sqlite3.h>
#include <stdlib.h>
#include <string.h>

// These wrappers are necessary because SQLITE_TRANSIENT
// is a pointer constant, and cgo doesn't translate them correctly.
// The definition in sqlite3.h is:
//
// typedef void (*sqlite3_destructor_type)(void*);
// #define SQLITE_STATIC      ((sqlite3_destructor_type)0)
// #define SQLITE_TRANSIENT   ((sqlite3_destructor_type)-1)

static int my_bind_text(sqlite3_stmt *stmt, int n, const char *p, int np) {
	return sqlite3_bind_text(stmt, n, p, np, SQLITE_TRANSIENT);
}
static int my_bind_blob(sqlite3_stmt *stmt, int n, void *p, int np) {
	return sqlite3_bind_blob(stmt, n, p, np, SQLITE_TRANSIENT);
}

// just to get ride of "warning: passing argument 5 of ‘sqlite3_prepare_v2’ from incompatible pointer type [...] ‘const char **’ but argument is of type ‘char **’"
static int my_prepare_v2(sqlite3 *db, const char *zSql, int nByte, sqlite3_stmt **ppStmt, char **pzTail) {
	return sqlite3_prepare_v2(db, zSql, nByte, ppStmt, (const char**)pzTail);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"reflect"
	"time"
	"unsafe"
)

type StmtError struct {
	ConnError
	s *Stmt
}

func (e *StmtError) SQL() string {
	return e.s.SQL()
}

func (s *Stmt) error(rv C.int, details ...string) error {
	if s == nil {
		return errors.New("nil sqlite statement")
	}
	if rv == C.SQLITE_OK {
		return nil
	}
	err := ConnError{c: s.c, code: Errno(rv), msg: C.GoString(C.sqlite3_errmsg(s.c.db))}
	if len(details) > 0 {
		err.details = details[0]
	}
	return &StmtError{err, s}
}

func (s *Stmt) specificError(msg string, a ...interface{}) error {
	return &StmtError{ConnError{c: s.c, code: ErrSpecific, msg: fmt.Sprintf(msg, a...)}, s}
}

// SQL statement
type Stmt struct {
	c                  *Conn
	stmt               *C.sqlite3_stmt
	sql                string
	tail               string
	columnCount        int
	cols               map[string]int // cached columns index by name
	bindParameterCount int
	params             map[string]int // cached parameter index by name
	// Enable type check in Scan methods
	CheckTypeMismatch bool
}

// Compile an SQL statement and optionally bind values.
// If an error occurs while binding values, the statement is returned (not finalized).
// Example:
//	stmt, err := db.Prepare("SELECT 1 where 1 = ?", 1)
//	if err != nil {
//		...
//	}
//	defer stmt.Finalize()
//
// (See sqlite3_prepare_v2: http://sqlite.org/c3ref/prepare.html)
func (c *Conn) Prepare(cmd string, args ...interface{}) (*Stmt, error) {
	if c == nil {
		return nil, errors.New("nil sqlite database")
	}
	cmdstr := C.CString(cmd)
	defer C.free(unsafe.Pointer(cmdstr))
	var stmt *C.sqlite3_stmt
	var tail *C.char
	rv := C.my_prepare_v2(c.db, cmdstr, -1, &stmt, &tail)
	if rv != C.SQLITE_OK {
		return nil, c.error(rv, cmd)
	}
	var t string
	if tail != nil && C.strlen(tail) > 0 {
		t = C.GoString(tail)
	}
	s := &Stmt{c: c, stmt: stmt, tail: t, columnCount: -1, bindParameterCount: -1, CheckTypeMismatch: true}
	if len(args) > 0 {
		err := s.Bind(args...)
		if err != nil {
			return s, err
		}
	}
	return s, nil
}

// One-step statement execution.
// Don't use it with SELECT or anything that returns data.
// The Stmt is reset at each call.
// (See http://sqlite.org/c3ref/bind_blob.html, http://sqlite.org/c3ref/step.html)
func (s *Stmt) Exec(args ...interface{}) error {
	err := s.Bind(args...)
	if err != nil {
		return err
	}
	return s.exec()
}
func (s *Stmt) exec() error {
	rv := C.sqlite3_step(s.stmt)
	C.sqlite3_reset(s.stmt)
	if Errno(rv) != Done {
		return s.error(rv)
	}
	return nil
}

// Like Exec but returns the number of rows that were changed or inserted or deleted.
// Don't use it with SELECT or anything that returns data.
// The Stmt is reset at each call.
func (s *Stmt) ExecDml(args ...interface{}) (int, error) {
	err := s.Exec(args...)
	if err != nil {
		return -1, err
	}
	return s.c.Changes(), nil
}

// Like ExecDml but returns the autoincremented rowid.
// Don't use it with SELECT or anything that returns data.
// The Stmt is reset at each call.
func (s *Stmt) Insert(args ...interface{}) (int64, error) {
	n, err := s.ExecDml(args...)
	if err != nil {
		return -1, err
	}
	if n == 0 { // No change => no insert...
		return -1, nil
	}
	return s.c.LastInsertRowid(), nil
}

// The callback function is invoked for each result row coming out of the statement.
//
//  s, err := c.Prepare(...)
//	// TODO error handling
//  defer s.Finalize()
//  err = s.Select(func(s *Stmt) error {
//  	//Scan
//  })
//	// TODO error handling
func (s *Stmt) Select(rowCallbackHandler func(s *Stmt) error) error {
	for {
		if ok, err := s.Next(); err != nil {
			return err
		} else if !ok {
			break
		}
		if err := rowCallbackHandler(s); err != nil {
			return err
		}
	}
	return nil
}

// Number of SQL parameters
// (See http://sqlite.org/c3ref/bind_parameter_count.html)
func (s *Stmt) BindParameterCount() int {
	if s.bindParameterCount == -1 {
		s.bindParameterCount = int(C.sqlite3_bind_parameter_count(s.stmt))
	}
	return s.bindParameterCount
}

// Index of a parameter with a given name (cached)
// The first host parameter has an index of 1, not 0.
// (See http://sqlite.org/c3ref/bind_parameter_index.html)
func (s *Stmt) BindParameterIndex(name string) (int, error) {
	if s.params == nil {
		count := s.BindParameterCount()
		s.params = make(map[string]int, count)
	}
	index, ok := s.params[name]
	if ok {
		return index, nil
	}
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	index = int(C.sqlite3_bind_parameter_index(s.stmt, cname))
	if index == 0 {
		return index, s.specificError("invalid parameter name: %s", name)
	}
	s.params[name] = index
	return index, nil
}

// Return the name of a wildcard parameter. (not cached)
// Return "" if the index is out of range or if the wildcard is unnamed.
// The first host parameter has an index of 1, not 0.
// (See http://sqlite.org/c3ref/bind_parameter_name.html)
func (s *Stmt) BindParameterName(i int) (string, error) {
	name := C.sqlite3_bind_parameter_name(s.stmt, C.int(i))
	if name == nil {
		return "", s.specificError("invalid parameter index: %d", i)
	}
	return C.GoString(name), nil
}

// Bind parameters by their name (name1, value1, ...)
func (s *Stmt) NamedBind(args ...interface{}) error {
	if len(args)%2 != 0 {
		return s.specificError("Expected an even number of arguments")
	}
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return s.specificError("non-string param name")
		}
		index, err := s.BindParameterIndex(name) // How to look up only once for one statement ?
		if err != nil {
			return err
		}
		err = s.BindByIndex(index, args[i+1])
		if err != nil {
			return err
		}
	}
	return nil
}

// Bind parameters by their index.
// Calls sqlite3_bind_parameter_count and sqlite3_bind_(blob|double|int|int64|null|text) depending on args type.
// (See http://sqlite.org/c3ref/bind_blob.html)
func (s *Stmt) Bind(args ...interface{}) error {
	n := s.BindParameterCount()
	if n != len(args) {
		return s.specificError("incorrect argument count for Stmt.Bind: have %d want %d", len(args), n)
	}

	for i, v := range args {
		err := s.BindByIndex(i+1, v)
		if err != nil {
			return err
		}
	}
	return nil
}

// Bind value to the specified host parameter of the prepared statement
// The leftmost SQL parameter has an index of 1.
func (s *Stmt) BindByIndex(index int, value interface{}) error {
	i := C.int(index)
	var rv C.int
	switch value := value.(type) {
	case nil:
		rv = C.sqlite3_bind_null(s.stmt, i)
	case string:
		cstr := C.CString(value)
		rv = C.my_bind_text(s.stmt, i, cstr, C.int(len(value)))
		C.free(unsafe.Pointer(cstr))
		//rv = C.my_bind_text(s.stmt, i, *((**C.char)(unsafe.Pointer(&value))), C.int(len(value)))
	case int:
		rv = C.sqlite3_bind_int(s.stmt, i, C.int(value))
	case int64:
		rv = C.sqlite3_bind_int64(s.stmt, i, C.sqlite3_int64(value))
	case byte:
		rv = C.sqlite3_bind_int(s.stmt, i, C.int(value))
	case bool:
		rv = C.sqlite3_bind_int(s.stmt, i, btocint(value))
	case float32:
		rv = C.sqlite3_bind_double(s.stmt, i, C.double(value))
	case float64:
		rv = C.sqlite3_bind_double(s.stmt, i, C.double(value))
	case []byte:
		var p *byte
		if len(value) > 0 {
			p = &value[0]
		}
		rv = C.my_bind_blob(s.stmt, i, unsafe.Pointer(p), C.int(len(value)))
	case time.Time: // At least three representations are possible: string (YYYY-MM-DD HH:MM:SS.SSS), int64 (unix time), float64 (julian day)
		// rv = C.my_bind_text(s.stmt, i, value.format("2006-01-02 15:04:05.000"))
		rv = C.sqlite3_bind_int64(s.stmt, i, C.sqlite3_int64(value.Unix()))
		// rv = C.sqlite3_bind_double(s.stmt, i, JulianDay(value))
	case ZeroBlobLength:
		rv = C.sqlite3_bind_zeroblob(s.stmt, i, C.int(value))
	default:
		return s.specificError("unsupported type in Bind: %s", reflect.TypeOf(value))
	}
	return s.error(rv)
}

// Evaluate an SQL statement
//
// With custom error handling:
//	for {
//		if ok, err := s.Next(); err != nil {
//			return nil, err
//		} else if !ok {
//			break
//		}
//		err = s.Scan(&fnum, &inum, &sstr)
//	}
// With panic on error:
// 	for Must(s.Next()) {
//		err := s.Scan(&fnum, &inum, &sstr)
//	}
//
// (See http://sqlite.org/c3ref/step.html)
func (s *Stmt) Next() (bool, error) {
	rv := C.sqlite3_step(s.stmt)
	err := Errno(rv)
	if err == Row {
		return true, nil
	}
	C.sqlite3_reset(s.stmt)
	if err != Done {
		return false, s.error(rv)
	}
	return false, nil
}

// Terminate the current execution of an SQL statement
// and reset it back to its starting state so that it can be reused.
// (See http://sqlite.org/c3ref/reset.html)
func (s *Stmt) Reset() error {
	return s.error(C.sqlite3_reset(s.stmt))
}

// Reset all bindings on a prepared statement
// (See http://sqlite.org/c3ref/clear_bindings.html)
func (s *Stmt) ClearBindings() error {
	return s.error(C.sqlite3_clear_bindings(s.stmt))
}

// Return the number of columns in the result set for the statement (with or without row)
// (See http://sqlite.org/c3ref/column_count.html)
func (s *Stmt) ColumnCount() int {
	if s.columnCount == -1 {
		s.columnCount = int(C.sqlite3_column_count(s.stmt))
	}
	return s.columnCount
}

// Return the number of values available from the current row of the currently executing statement.
// Same as ColumnCount() except when there is no (more) row, it returns 0
// (See http://sqlite.org/c3ref/data_count.html)
func (s *Stmt) DataCount() int {
	return int(C.sqlite3_data_count(s.stmt))
}

// Return the name of the Nth column of the result set returned by the SQL statement. (not cached)
// The leftmost column is number 0.
// (See http://sqlite.org/c3ref/column_name.html)
func (s *Stmt) ColumnName(index int) string {
	// If there is no AS clause then the name of the column is unspecified and may change from one release of SQLite to the next.
	return C.GoString(C.sqlite3_column_name(s.stmt, C.int(index)))
}

// Return the name of the columns of the result set returned by the SQL statement. (not cached)
func (s *Stmt) ColumnNames() []string {
	count := s.ColumnCount()
	names := make([]string, count)
	for i := 0; i < count; i++ {
		names[i] = s.ColumnName(i)
	}
	return names
}

// SQLite fundamental datatypes
type Type int

func (t Type) String() string {
	return typeText[t]
}

var (
	Integer Type = Type(C.SQLITE_INTEGER)
	Float   Type = Type(C.SQLITE_FLOAT)
	Blob    Type = Type(C.SQLITE_BLOB)
	Null    Type = Type(C.SQLITE_NULL)
	Text    Type = Type(C.SQLITE3_TEXT)
)

var typeText = map[Type]string{
	Integer: "Integer",
	Float:   "Float",
	Blob:    "Blob",
	Null:    "Null",
	Text:    "Text",
}

// Return the datatype code for the initial data type of the result column.
// The leftmost column is number 0.
// Should not be cached (valid only for one row) (see dynamic type http://www.sqlite.org/datatype3.html)
//
// After a type conversion, the value returned by sqlite3_column_type() is undefined.
// (See sqlite3_column_type: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ColumnType(index int) Type {
	return Type(C.sqlite3_column_type(s.stmt, C.int(index)))
}

// Scan result values from a query by name (name1, value1, ...)
// Example:
//	stmt, err := db.Prepare("SELECT 1 as id, 'test' as name")
//	// TODO error handling
//	defer stmt.Finalize()
//	var id int
//	var name string
//  err = s.Select(func(s *Stmt) (err error) {
//		if err = stmt.NamedScan("name", &name, "id", &id); err != nil {
//			return
//      }
//		fmt.Println(id, name)
//  	return
//  })
//	// TODO error handling
//
// NULL value is converted to 0 if arg type is *int,*int64,*float,*float64, to "" for *string, to []byte{} for *[]byte and to false for *bool.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on args type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) NamedScan(args ...interface{}) error {
	if len(args)%2 != 0 {
		return s.specificError("Expected an even number of arguments")
	}
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return s.specificError("non-string field name")
		}
		index, err := s.ColumnIndex(name) // How to look up only once for one statement ?
		if err != nil {
			return err
		}
		ptr := args[i+1]
		_, err = s.ScanByIndex(index, ptr)
		if err != nil {
			return err
		}
	}
	return nil
}

// Scan result values from a query
// Example:
//	stmt, err := db.Prepare("SELECT 1, 'test'")
//	// TODO error handling
//	defer stmt.Finalize()
//	var id int
//	var name string
//  err = s.Select(func(s *Stmt) error {
//		if err = stmt.Scan(&id, &name); err != nil {
//			return
//      }
//		fmt.Println(id, name)
//  	return
//  })
//	// TODO error handling
//
// NULL value is converted to 0 if arg type is *int,*int64,*float,*float64, to "" for *string, to []byte{} for *[]byte and to false for *bool.
// To avoid NULL conversion, arg type must be **T
// Calls sqlite3_column_(blob|double|int|int64|text) depending on args type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) Scan(args ...interface{}) error {
	n := s.ColumnCount()
	if n != len(args) { // What happens when the number of arguments is less than the number of columns?
		return s.specificError("incorrect argument count for Stmt.Scan: have %d want %d", len(args), n)
	}

	for i, v := range args {
		_, err := s.ScanByIndex(i, v)
		if err != nil {
			return err
		}
	}
	return nil
}

// Return the SQL associated with a prepared statement.
// (See http://sqlite.org/c3ref/sql.html)
func (s *Stmt) SQL() string {
	if s.sql == "" {
		s.sql = C.GoString(C.sqlite3_sql(s.stmt))
	}
	return s.sql
}

// Column index in a result set for a given column name
// The leftmost column is number 0.
// Must scan all columns (but result is cached).
// (See http://sqlite.org/c3ref/column_name.html)
func (s *Stmt) ColumnIndex(name string) (int, error) {
	if s.cols == nil {
		count := s.ColumnCount()
		s.cols = make(map[string]int, count)
		for i := 0; i < count; i++ {
			s.cols[s.ColumnName(i)] = i
		}
	}
	index, ok := s.cols[name]
	if ok {
		return index, nil
	}
	return -1, s.specificError("invalid column name: %s", name)
}

// Returns true when column is null.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on arg type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanByName(name string, value interface{}) (bool, error) {
	index, err := s.ColumnIndex(name)
	if err != nil {
		return false, err
	}
	return s.ScanByIndex(index, value)
}

// The leftmost column/index is number 0.
//
// Destination type is specified by the caller (except when value type is *interface{}).
// The value must be of one of the following types:
//    (*)*string,
//    (*)*int, (*)*int64, (*)*byte,
//    (*)*bool
//    (*)*float64
//    (*)*[]byte
//    *interface{}
//
// Returns true when column is null.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on arg type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanByIndex(index int, value interface{}) (bool, error) {
	var isNull bool
	var err error
	switch value := value.(type) {
	case nil:
	case *string:
		*value, isNull = s.ScanText(index)
	case **string:
		var st string
		st, isNull = s.ScanText(index)
		if isNull {
			*value = nil
		} else {
			**value = st
		}
	case *int:
		*value, isNull, err = s.ScanInt(index)
	case **int:
		var i int
		i, isNull, err = s.ScanInt(index)
		if err == nil {
			if isNull {
				*value = nil
			} else {
				**value = i
			}
		}
	case *int64:
		*value, isNull, err = s.ScanInt64(index)
	case **int64:
		var i int64
		i, isNull, err = s.ScanInt64(index)
		if err == nil {
			if isNull {
				*value = nil
			} else {
				**value = i
			}
		}
	case *byte:
		*value, isNull, err = s.ScanByte(index)
	case **byte:
		var b byte
		b, isNull, err = s.ScanByte(index)
		if err == nil {
			if isNull {
				*value = nil
			} else {
				**value = b
			}
		}
	case *bool:
		*value, isNull, err = s.ScanBool(index)
	case **bool:
		var b bool
		b, isNull, err = s.ScanBool(index)
		if err == nil {
			if isNull {
				*value = nil
			} else {
				**value = b
			}
		}
	case *float64:
		*value, isNull, err = s.ScanDouble(index)
	case **float64:
		var f float64
		f, isNull, err = s.ScanDouble(index)
		if err == nil {
			if isNull {
				*value = nil
			} else {
				**value = f
			}
		}
	case *[]byte:
		*value, isNull = s.ScanBlob(index)
	case **[]byte:
		var bs []byte
		bs, isNull = s.ScanBlob(index)
		if isNull {
			*value = nil
		} else {
			**value = bs
		}
	case *time.Time: // go fix doesn't like this type!
		*value, isNull, err = s.ScanTime(index)
	case *interface{}:
		*value = s.ScanValue(index)
		isNull = *value == nil
	default:
		return false, s.specificError("unsupported type in Scan: %s", reflect.TypeOf(value))
	}
	return isNull, err
}

// The leftmost column/index is number 0.
//
// Destination type is decided by SQLite.
// The returned value will be of one of the following types:
//    nil
//    string
//    int64
//    float64
//    []byte
//
// Calls sqlite3_column_(blob|double|int|int64|text) depending on columns type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanValue(index int) (value interface{}) {
	switch s.ColumnType(index) {
	case Null:
		value = nil
	case Text:
		p := C.sqlite3_column_text(s.stmt, C.int(index))
		value = C.GoString((*C.char)(unsafe.Pointer(p)))
	case Integer:
		value = int64(C.sqlite3_column_int64(s.stmt, C.int(index)))
	case Float:
		value = float64(C.sqlite3_column_double(s.stmt, C.int(index)))
	case Blob:
		p := C.sqlite3_column_blob(s.stmt, C.int(index))
		n := C.sqlite3_column_bytes(s.stmt, C.int(index))
		value = (*[1 << 30]byte)(unsafe.Pointer(p))[0:n]
	default:
		panic("The column type is not one of SQLITE_INTEGER, SQLITE_FLOAT, SQLITE_TEXT, SQLITE_BLOB, or SQLITE_NULL")
	}
	return
}

// Like ScanValue on several columns
func (s *Stmt) ScanValues(values []interface{}) {
	for i := range values {
		values[i] = s.ScanValue(i)
	}
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_text: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanText(index int) (value string, isNull bool) {
	p := C.sqlite3_column_text(s.stmt, C.int(index))
	if p == nil {
		isNull = true
	} else {
		value = C.GoString((*C.char)(unsafe.Pointer(p)))
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_int: http://sqlite.org/c3ref/column_blob.html)
// TODO Factorize with ScanByte, ScanBool
func (s *Stmt) ScanInt(index int) (value int, isNull bool, err error) {
	ctype := s.ColumnType(index)
	if ctype == Null {
		isNull = true
	} else {
		if s.CheckTypeMismatch {
			err = s.checkTypeMismatch(ctype, Integer)
		}
		value = int(C.sqlite3_column_int(s.stmt, C.int(index)))
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_int64: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanInt64(index int) (value int64, isNull bool, err error) {
	ctype := s.ColumnType(index)
	if ctype == Null {
		isNull = true
	} else {
		if s.CheckTypeMismatch {
			err = s.checkTypeMismatch(ctype, Integer)
		}
		value = int64(C.sqlite3_column_int64(s.stmt, C.int(index)))
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_int: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanByte(index int) (value byte, isNull bool, err error) {
	ctype := s.ColumnType(index)
	if ctype == Null {
		isNull = true
	} else {
		if s.CheckTypeMismatch {
			err = s.checkTypeMismatch(ctype, Integer)
		}
		value = byte(C.sqlite3_column_int(s.stmt, C.int(index)))
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_int: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanBool(index int) (value bool, isNull bool, err error) {
	ctype := s.ColumnType(index)
	if ctype == Null {
		isNull = true
	} else {
		if s.CheckTypeMismatch {
			err = s.checkTypeMismatch(ctype, Integer)
		}
		value = C.sqlite3_column_int(s.stmt, C.int(index)) == 1
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_double: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanDouble(index int) (value float64, isNull bool, err error) {
	ctype := s.ColumnType(index)
	if ctype == Null {
		isNull = true
	} else {
		if s.CheckTypeMismatch {
			err = s.checkTypeMismatch(ctype, Float)
		}
		value = float64(C.sqlite3_column_double(s.stmt, C.int(index)))
	}
	return
}

// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_blob: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanBlob(index int) (value []byte, isNull bool) {
	p := C.sqlite3_column_blob(s.stmt, C.int(index))
	if p == nil {
		isNull = true
	} else {
		n := C.sqlite3_column_bytes(s.stmt, C.int(index))
		value = (*[1 << 30]byte)(unsafe.Pointer(p))[0:n]
	}
	return
}

func (s *Stmt) ScanTime(index int) (value time.Time, isNull bool, err error) {
	switch s.ColumnType(index) {
	case Null:
		isNull = true
	case Text:
		p := C.sqlite3_column_text(s.stmt, C.int(index))
		txt := C.GoString((*C.char)(unsafe.Pointer(p)))
		var layout string
		switch len(txt) {
		case 5: // HH:MM
			layout = "15:04"
		case 8: // HH:MM:SS
			layout = "15:04:05"
		case 10: // YYYY-MM-DD
			layout = "2006-01-02"
		case 12: // HH:MM:SS.SSS
			layout = "15:04:05.000"
		case 16: // YYYY-MM-DDTHH:MM
			if txt[10] == 'T' {
				layout = "2006-01-02T15:04"
			} else {
				layout = "2006-01-02 15:04"
			}
		case 19: // YYYY-MM-DDTHH:MM:SS
			if txt[10] == 'T' {
				layout = "2006-01-02T15:04:05"
			} else {
				layout = "2006-01-02 15:04:05"
			}
		default: // YYYY-MM-DDTHH:MM:SS.SSS or parse error
			if len(txt) > 10 && txt[10] == 'T' {
				layout = "2006-01-02T15:04:05.000"
			} else {
				layout = "2006-01-02 15:04:05.000"
			}
		}
		value, err = time.Parse(layout, txt)
	case Integer:
		unixepoch := int64(C.sqlite3_column_int64(s.stmt, C.int(index)))
		value = time.Unix(unixepoch, 0)
	case Float:
		jd := float64(C.sqlite3_column_double(s.stmt, C.int(index)))
		value = JulianDayToUTC(jd)
	default:
		panic("The column type is not one of SQLITE_INTEGER, SQLITE_FLOAT, SQLITE_TEXT, or SQLITE_NULL")
	}
	return
}

// Only lossy conversion is reported as error.
func (s *Stmt) checkTypeMismatch(source, target Type) error {
	switch target {
	case Integer:
		switch source {
		case Float:
			fallthrough
		case Text:
			fallthrough
		case Blob:
			return s.specificError("Type mismatch, source %s vs target %s", source, target)
		}
	case Float:
		switch source {
		case Text:
			fallthrough
		case Blob:
			return s.specificError("Type mismatch, source %s vs target %s", source, target)
		}
	}
	return nil
}

// Return true if the prepared statement is in need of being reset.
// (See http://sqlite.org/c3ref/stmt_busy.html)
func (s *Stmt) Busy() bool {
	return C.sqlite3_stmt_busy(s.stmt) != 0
}

// Destroy a prepared statement
// (See http://sqlite.org/c3ref/finalize.html)
func (s *Stmt) Finalize() error {
	rv := C.sqlite3_finalize(s.stmt)
	if rv != C.SQLITE_OK {
		return s.error(rv)
	}
	s.stmt = nil
	return nil
}

// Find the database handle of a prepared statement
// (Like http://sqlite.org/c3ref/db_handle.html)
func (s *Stmt) Conn() *Conn {
	return s.c
}

// Return true if the prepared statement is guaranteed to not modify the database.
// (See http://sqlite.org/c3ref/stmt_readonly.html)
func (s *Stmt) ReadOnly() bool {
	return C.sqlite3_stmt_readonly(s.stmt) == 1
}