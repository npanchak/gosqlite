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
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
	"unsafe"
)

type StmtError struct {
	ConnError
	s *Stmt
}

// SQL returns the SQL associated with the prepared statement in error.
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
// (See http://sqlite.org/c3ref/stmt.html)
type Stmt struct {
	c                  *Conn
	stmt               *C.sqlite3_stmt
	sql                string
	tail               string
	columnCount        int
	cols               map[string]int // cached columns index by name
	bindParameterCount int
	params             map[string]int // cached parameter index by name
	// Enable type check in Scan methods (default true)
	CheckTypeMismatch bool
	// Tell if the stmt should be cached (default true)
	Cacheable bool
}

func (c *Conn) prepare(cmd string, args ...interface{}) (*Stmt, error) {
	if c == nil {
		return nil, errors.New("nil sqlite database")
	}
	cmdstr := C.CString(cmd)
	defer C.free(unsafe.Pointer(cmdstr))
	var stmt *C.sqlite3_stmt
	var tail *C.char
	// If the caller knows that the supplied string is nul-terminated, then there is a small performance advantage to be gained by passing an nByte parameter that is equal to the number of bytes in the input string including the nul-terminator bytes as this saves SQLite from having to make a copy of the input string.
	rv := C.my_prepare_v2(c.db, cmdstr, C.int(len(cmd)+1), &stmt, &tail)
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
			s.finalize()
			return nil, err
		}
	}
	return s, nil
}

// Prepare first looks in the statement cache or compiles the SQL statement.
// And optionally bind values.
// (See sqlite3_prepare_v2: http://sqlite.org/c3ref/prepare.html)
func (c *Conn) Prepare(cmd string, args ...interface{}) (*Stmt, error) {
	s := c.stmtCache.find(cmd)
	if s != nil {
		if len(args) > 0 {
			err := s.Bind(args...)
			if err != nil {
				s.finalize() // don't put it back in the cache
				return nil, err
			}
		}
		return s, nil
	}
	s, err := c.prepare(cmd, args...)
	if s != nil {
		s.Cacheable = true
	}
	return s, err
}

// Exec is a one-step statement execution.
// Don't use it with SELECT or anything that returns data.
// The Stmt is reset at each call.
// (See http://sqlite.org/c3ref/bind_blob.html, http://sqlite.org/c3ref/step.html)
func (s *Stmt) Exec(args ...interface{}) error {
	// TODO Check column count == 0
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
		return s.error(rv, "Stmt.exec")
	}
	return nil
}

// ExecDml is like Exec but returns the number of rows that were changed or inserted or deleted.
// Don't use it with SELECT or anything that returns data.
// The Stmt is reset at each call.
func (s *Stmt) ExecDml(args ...interface{}) (int, error) {
	err := s.Exec(args...)
	if err != nil {
		return -1, err
	}
	return s.c.Changes(), nil
}

// Insert is like ExecDml but returns the autoincremented rowid.
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

// Select helps executing SELECT statement:
// (1) it binds the specified args,
// (2) it steps on the rows returned,
// (3) it delegates scanning to a callback function.
// The callback function is invoked for each result row coming out of the statement.
//
//  s, err := db.Prepare(...)
//	// TODO error handling
//  defer s.Finalize()
//  err = s.Select(func(s *Stmt) error {
//  	//Scan
//  })
//	// TODO error handling
func (s *Stmt) Select(rowCallbackHandler func(s *Stmt) error, args ...interface{}) error {
	if len(args) > 0 {
		err := s.Bind(args...)
		if err != nil {
			return err
		}
	}
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

// SelectOneRow helps executing SELECT statement that is expected to return only one row.
// Args are for scanning (not binding).
// Returns false if there is no matching row.
// No check is done to ensure that no more than one row is returned by the statement.
// TODO Create a SelectUniqueRow that checks that the row is unique.
func (s *Stmt) SelectOneRow(args ...interface{}) (bool, error) {
	if ok, err := s.Next(); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}
	return true, s.Scan(args...)
}

// BindParameterCount returns the number of SQL parameters.
// FIXME If parameters of the ?NNN form are used, there may be gaps in the list.
// (See http://sqlite.org/c3ref/bind_parameter_count.html)
func (s *Stmt) BindParameterCount() int {
	if s.bindParameterCount == -1 {
		s.bindParameterCount = int(C.sqlite3_bind_parameter_count(s.stmt))
	}
	return s.bindParameterCount
}

// BindParameterIndex returns the index of a parameter with a given name (cached).
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
		return index, s.specificError("invalid parameter name: %q", name)
	}
	s.params[name] = index
	return index, nil
}

// BindParameterName returns the name of a wildcard parameter (not cached).
// Returns "" if the index is out of range or if the wildcard is unnamed.
// The first host parameter has an index of 1, not 0.
// (See http://sqlite.org/c3ref/bind_parameter_name.html)
func (s *Stmt) BindParameterName(i int) (string, error) {
	name := C.sqlite3_bind_parameter_name(s.stmt, C.int(i))
	if name == nil {
		return "", s.specificError("invalid parameter index: %d", i)
	}
	return C.GoString(name), nil
}

// NamedBind binds parameters by their name (name1, value1, ...)
func (s *Stmt) NamedBind(args ...interface{}) error {
	if len(args)%2 != 0 {
		return s.specificError("expected an even number of arguments: %d", len(args))
	}
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return s.specificError("non-string param name at %d: %T", i, args[i])
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

// Bind binds parameters by their index.
// Calls sqlite3_bind_parameter_count and sqlite3_bind_(blob|double|int|int64|null|text) depending on args type/kind.
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

// NullIfEmpty transforms empty string to null when true (true by default)
var NullIfEmptyString = true

// NullIfZeroTime transforms zero time (time.Time.IsZero) to null when true (true by default)
var NullIfZeroTime = true

// BindByIndex binds value to the specified host parameter of the prepared statement.
// Value's type/kind is used to find the storage class.
// The leftmost SQL parameter has an index of 1.
func (s *Stmt) BindByIndex(index int, value interface{}) error {
	i := C.int(index)
	var rv C.int
	switch value := value.(type) {
	case nil:
		rv = C.sqlite3_bind_null(s.stmt, i)
	case string:
		if NullIfEmptyString && len(value) == 0 {
			rv = C.sqlite3_bind_null(s.stmt, i)
		} else {
			cs, l := cstring(value)
			rv = C.my_bind_text(s.stmt, i, cs, l)
		}
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
	case time.Time:
		if NullIfZeroTime && value.IsZero() {
			rv = C.sqlite3_bind_null(s.stmt, i)
		} else {
			rv = C.sqlite3_bind_int64(s.stmt, i, C.sqlite3_int64(value.Unix()))
		}
	case ZeroBlobLength:
		rv = C.sqlite3_bind_zeroblob(s.stmt, i, C.int(value))
	case driver.Valuer:
		v, err := value.Value()
		if err != nil {
			return err
		}
		return s.BindByIndex(index, v)
	default:
		return s.BindReflect(index, value)
	}
	return s.error(rv, "Stmt.Bind")
}

// BindReflect binds value to the specified host parameter of the prepared statement.
// Value's (reflect) Kind is used to find the storage class.
// The leftmost SQL parameter has an index of 1.
func (s *Stmt) BindReflect(index int, value interface{}) error {
	i := C.int(index)
	var rv C.int
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.String:
		cs, l := cstring(v.String())
		rv = C.my_bind_text(s.stmt, i, cs, l)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv = C.sqlite3_bind_int64(s.stmt, i, C.sqlite3_int64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		ui := v.Uint()
		if ui > math.MaxInt64 {
			return s.specificError("int overflow")
		}
		rv = C.sqlite3_bind_int64(s.stmt, i, C.sqlite3_int64(ui))
	case reflect.Bool:
		rv = C.sqlite3_bind_int(s.stmt, i, btocint(v.Bool()))
	case reflect.Float32, reflect.Float64:
		rv = C.sqlite3_bind_double(s.stmt, i, C.double(v.Float()))
	default:
		name, _ := s.BindParameterName(index)
		return s.specificError("unsupported type in Bind: %T (index: %d, name: %q)", value, index, name)
	}
	return s.error(rv, "Stmt.Bind")
}

// Next evaluates an SQL statement
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
	C.sqlite3_reset(s.stmt) // Release implicit lock as soon as possible (see dbEvalStep in tclsqlite3.c)
	if err != Done {
		return false, s.error(rv, "Stmt.Next")
	}
	// TODO Check column count > 0
	return false, nil
}

// Reset terminates the current execution of an SQL statement
// and reset it back to its starting state so that it can be reused.
// (See http://sqlite.org/c3ref/reset.html)
func (s *Stmt) Reset() error {
	return s.error(C.sqlite3_reset(s.stmt), "Stmt.Reset")
}

// ClearBindings resets all bindings on a prepared statement.
// (See http://sqlite.org/c3ref/clear_bindings.html)
func (s *Stmt) ClearBindings() error {
	return s.error(C.sqlite3_clear_bindings(s.stmt), "Stmt.ClearBindings")
}

// ColumnCount returns the number of columns in the result set for the statement (with or without row).
// (See http://sqlite.org/c3ref/column_count.html)
func (s *Stmt) ColumnCount() int {
	if s.columnCount == -1 {
		s.columnCount = int(C.sqlite3_column_count(s.stmt))
	}
	return s.columnCount
}

// DataCount returns the number of values available from the current row of the currently executing statement.
// Same as ColumnCount() except when there is no (more) row, it returns 0.
// (See http://sqlite.org/c3ref/data_count.html)
func (s *Stmt) DataCount() int {
	return int(C.sqlite3_data_count(s.stmt))
}

// ColumnName returns the name of the Nth column of the result set returned by the SQL statement. (not cached)
// The leftmost column is number 0.
// (See http://sqlite.org/c3ref/column_name.html)
func (s *Stmt) ColumnName(index int) string {
	// If there is no AS clause then the name of the column is unspecified and may change from one release of SQLite to the next.
	return C.GoString(C.sqlite3_column_name(s.stmt, C.int(index)))
}

// ColumnNames returns the name of the columns of the result set returned by the SQL statement. (not cached)
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

const (
	Integer = Type(C.SQLITE_INTEGER)
	Float   = Type(C.SQLITE_FLOAT)
	Blob    = Type(C.SQLITE_BLOB)
	Null    = Type(C.SQLITE_NULL)
	Text    = Type(C.SQLITE3_TEXT)
)

var typeText = map[Type]string{
	Integer: "Integer",
	Float:   "Float",
	Blob:    "Blob",
	Null:    "Null",
	Text:    "Text",
}

// ColumnType returns the datatype code for the initial data type of the result column.
// The leftmost column is number 0.
// Should not be cached (valid only for one row) (see dynamic type http://www.sqlite.org/datatype3.html)
//
// After a type conversion, the value returned by sqlite3_column_type() is undefined.
// (See sqlite3_column_type: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ColumnType(index int) Type {
	return Type(C.sqlite3_column_type(s.stmt, C.int(index)))
}

// NamedScan scans result values from a query by name (name1, value1, ...).
//
// NULL value is converted to 0 if arg type is *int,*int64,*float,*float64, to "" for *string, to []byte{} for *[]byte and to false for *bool.
// To avoid NULL conversion, arg type must be **T.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on args type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) NamedScan(args ...interface{}) error {
	if len(args)%2 != 0 {
		return s.specificError("expected an even number of arguments: %d", len(args))
	}
	for i := 0; i < len(args); i += 2 {
		name, ok := args[i].(string)
		if !ok {
			return s.specificError("non-string field name at %d: %T", i, args[i])
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

// Scan scans result values from a query.
//
// NULL value is converted to 0 if arg type is *int,*int64,*float,*float64, to "" for *string, to []byte{} for *[]byte and to false for *bool.
// To avoid NULL conversion, arg type must be **T.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on args type/kind.
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

// SQL returns the SQL associated with a prepared statement.
// (See http://sqlite.org/c3ref/sql.html)
func (s *Stmt) SQL() string {
	if s.sql == "" {
		s.sql = C.GoString(C.sqlite3_sql(s.stmt))
	}
	return s.sql
}

// ColumnIndex returns the column index in a result set for a given column name.
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

// ScanByName scans result value from a query.
// Returns true when column is null.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on arg type/kind.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanByName(name string, value interface{}) (bool, error) {
	index, err := s.ColumnIndex(name)
	if err != nil {
		return false, err
	}
	return s.ScanByIndex(index, value)
}

// ScanByIndex scans result value from a query.
// The leftmost column/index is number 0.
//
// Destination type is specified by the caller (except when value type is *interface{}).
// The value must be of one of the following types/kinds:
//    (*)*string
//    (*)*int,int8,int16,int32,int64
//    (*)*uint,uint8,uint16,uint32,uint64
//    (*)*bool
//    (*)*float32,float64
//    (*)*[]byte
//    *time.Time
//    sql.Scanner
//    *interface{}
//
// Returns true when column is null.
// Calls sqlite3_column_(blob|double|int|int64|text) depending on arg type/kind.
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
	case sql.Scanner:
		var v interface{}
		v, isNull = s.ScanValue(index, false)
		err = value.Scan(v)
	case *interface{}:
		*value, isNull = s.ScanValue(index, false)
	default:
		return s.ScanReflect(index, value)
	}
	return isNull, err
}

// ScanReflect scans result value from a query.
// The leftmost column/index is number 0.
//
// Destination type is specified by the caller.
// The value must be of one of the following kinds:
//    *string
//    *int,int8,int16,int32,int64
//    *uint,uint8,uint16,uint32,uint64
//    *bool
//    *float32,float64
//
// Returns true when column is null.
func (s *Stmt) ScanReflect(index int, v interface{}) (bool, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return false, s.specificError("ScanReflect unsupported type %T", v)
	}
	var isNull bool
	var err error
	dv := reflect.Indirect(rv)
	switch dv.Kind() {
	case reflect.String:
		var t string
		t, isNull = s.ScanText(index)
		dv.SetString(t)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var i int64
		i, isNull, err = s.ScanInt64(index)
		if err == nil {
			dv.SetInt(i)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		var i int64
		i, isNull, err = s.ScanInt64(index)
		if err == nil {
			if i < 0 {
				err = s.specificError("negative value: %d", i)
			} else {
				dv.SetUint(uint64(i))
			}
		}
	case reflect.Bool:
		var b bool
		b, isNull, err = s.ScanBool(index)
		if err == nil {
			dv.SetBool(b)
		}
	case reflect.Float32, reflect.Float64:
		var f float64
		f, isNull, err = s.ScanDouble(index)
		if err == nil {
			dv.SetFloat(f)
		}
	default:
		return false, s.specificError("unsupported type in Scan: %T", v)
	}
	return isNull, err
}

// ScanValue scans result value from a query.
// The leftmost column/index is number 0.
//
// Destination type is decided by SQLite.
// The returned value will be of one of the following types:
//    nil
//    string (exception if blob is true)
//    int64
//    float64
//    []byte
//
// Calls sqlite3_column_(blob|double|int|int64|text) depending on columns type.
// (See http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanValue(index int, blob bool) (interface{}, bool) {
	switch s.ColumnType(index) {
	case Null:
		return nil, true
	case Text:
		if blob {
			p := C.sqlite3_column_blob(s.stmt, C.int(index))
			n := C.sqlite3_column_bytes(s.stmt, C.int(index))
			return C.GoBytes(p, n), false
		}
		p := C.sqlite3_column_text(s.stmt, C.int(index))
		return C.GoString((*C.char)(unsafe.Pointer(p))), false
	case Integer:
		return int64(C.sqlite3_column_int64(s.stmt, C.int(index))), false
	case Float:
		return float64(C.sqlite3_column_double(s.stmt, C.int(index))), false
	case Blob:
		p := C.sqlite3_column_blob(s.stmt, C.int(index))
		n := C.sqlite3_column_bytes(s.stmt, C.int(index))
		// value = (*[1 << 30]byte)(unsafe.Pointer(p))[:n]
		return C.GoBytes(p, n), false // The memory space used to hold strings and BLOBs is freed automatically.
	}
	panic("The column type is not one of SQLITE_INTEGER, SQLITE_FLOAT, SQLITE_TEXT, SQLITE_BLOB, or SQLITE_NULL")
}

// ScanValues is like ScanValue on several columns.
func (s *Stmt) ScanValues(values []interface{}) {
	for i := range values {
		values[i], _ = s.ScanValue(i, false)
	}
}

// ScanText scans result value from a query.
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

// ScanInt scans result value from a query.
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

// ScanInt64 scans result value from a query.
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

// ScanByte scans result value from a query.
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

// ScanBool scans result value from a query.
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

// ScanDouble scans result value from a query.
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

// ScanBlob scans result value from a query.
// The leftmost column/index is number 0.
// Returns true when column is null.
// (See sqlite3_column_blob: http://sqlite.org/c3ref/column_blob.html)
func (s *Stmt) ScanBlob(index int) (value []byte, isNull bool) {
	p := C.sqlite3_column_blob(s.stmt, C.int(index))
	if p == nil {
		isNull = true
	} else {
		n := C.sqlite3_column_bytes(s.stmt, C.int(index))
		// value = (*[1 << 30]byte)(unsafe.Pointer(p))[:n]
		value = C.GoBytes(p, n) // The memory space used to hold strings and BLOBs is freed automatically.
	}
	return
}

// ScanTime scans result value from a query.
// If time is persisted as string without timezone, UTC is used.
// If time is persisted as numeric, local is used.
// The leftmost column/index is number 0.
// Returns true when column is null.
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
		case 23: // YYYY-MM-DDTHH:MM:SS.SSS
			if txt[10] == 'T' {
				layout = "2006-01-02T15:04:05.999"
			} else {
				layout = "2006-01-02 15:04:05.999"
			}
		default: // YYYY-MM-DDTHH:MM:SS.SSSZhh:mm or parse error
			if len(txt) > 10 && txt[10] == 'T' {
				layout = "2006-01-02T15:04:05.999Z07:00"
			} else {
				layout = "2006-01-02 15:04:05.999Z07:00"
			}
		}
		value, err = time.Parse(layout, txt) // UTC except when timezone is specified
	case Integer:
		unixepoch := int64(C.sqlite3_column_int64(s.stmt, C.int(index)))
		value = time.Unix(unixepoch, 0) // local time
	case Float:
		jd := float64(C.sqlite3_column_double(s.stmt, C.int(index)))
		value = JulianDayToLocalTime(jd) // local time
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
			return s.specificError("type mismatch, source %s vs target %s", source, target)
		}
	case Float:
		switch source {
		case Text:
			fallthrough
		case Blob:
			return s.specificError("type mismatch, source %s vs target %s", source, target)
		}
	}
	return nil
}

// Busy returns true if the prepared statement is in need of being reset.
// (See http://sqlite.org/c3ref/stmt_busy.html)
func (s *Stmt) Busy() bool {
	return C.sqlite3_stmt_busy(s.stmt) != 0
}

// Finalize destroys a prepared statement.
// (See http://sqlite.org/c3ref/finalize.html)
func (s *Stmt) Finalize() error {
	if s == nil {
		return errors.New("nil sqlite statement")
	}
	if s.Cacheable && s.c != nil && s.c.db != nil {
		return s.c.stmtCache.release(s)
	}
	return s.finalize()
}
func (s *Stmt) finalize() error {
	if s == nil {
		return errors.New("nil sqlite statement")
	}
	if s.stmt == nil {
		return nil
	}
	if s.c == nil || s.c.db == nil {
		Log(C.SQLITE_MISUSE, "sqlite statement with already closed database connection")
		return errors.New("sqlite statement with already closed database connection")
	}
	rv := C.sqlite3_finalize(s.stmt)
	if rv != C.SQLITE_OK {
		Log(int(rv), "error while finalizing Stmt")
		return s.error(rv, "Stmt.finalize")
	}
	s.stmt = nil
	return nil
}

// Conn finds the database handle of a prepared statement.
// (Like http://sqlite.org/c3ref/db_handle.html)
func (s *Stmt) Conn() *Conn {
	return s.c
}

// ReadOnly returns true if the prepared statement is guaranteed to not modify the database.
// (See http://sqlite.org/c3ref/stmt_readonly.html)
func (s *Stmt) ReadOnly() bool {
	return C.sqlite3_stmt_readonly(s.stmt) == 1
}
