// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite_test

import (
	"fmt"
	. "github.com/gwenn/gosqlite"
	"testing"
)

type testModule struct {
	t        *testing.T
	intarray []int
}

type testVTab struct {
	intarray []int
}

type testVTabCursor struct {
	vTab  *testVTab
	index int /* Current cursor position */
}

func (m testModule) Create(c *Conn, args []string) (VTab, error) {
	//println("testVTab.Create")
	assert(m.t, "Six arguments expected", len(args) == 6)
	assertEquals(m.t, "Expected '%s' but got '%s' as module name", "test", args[0])
	assertEquals(m.t, "Expected '%s' but got '%s' as db name", "main", args[1])
	assertEquals(m.t, "Expected '%s' but got '%s' as table name", "vtab", args[2])
	assertEquals(m.t, "Expected '%s' but got '%s' as first arg", "'1'", args[3])
	assertEquals(m.t, "Expected '%s' but got '%s' as first arg", "2", args[4])
	assertEquals(m.t, "Expected '%s' but got '%s' as first arg", "three", args[5])
	err := c.DeclareVTab("CREATE TABLE x(test TEXT)")
	if err != nil {
		return nil, err
	}
	return &testVTab{m.intarray}, nil
}
func (m testModule) Connect(c *Conn, args []string) (VTab, error) {
	//println("testVTab.Connect")
	return m.Create(c, args)
}

func (m testModule) Destroy() {
	//println("testModule.Destroy")
}

func (v *testVTab) BestIndex() error {
	//fmt.Printf("testVTab.BestIndex: %v\n", v)
	return nil
}
func (v *testVTab) Disconnect() error {
	//fmt.Printf("testVTab.Disconnect: %v\n", v)
	return nil
}
func (v *testVTab) Destroy() error {
	//fmt.Printf("testVTab.Destroy: %v\n", v)
	return nil
}
func (v *testVTab) Open() (VTabCursor, error) {
	//fmt.Printf("testVTab.Open: %v\n", v)
	return &testVTabCursor{v, 0}, nil
}

func (vc *testVTabCursor) Close() error {
	//fmt.Printf("testVTabCursor.Close: %v\n", vc)
	return nil
}
func (vc *testVTabCursor) Filter( /*idxNum int, idxStr string, int argc, sqlite3_value **argv*/) error {
	//fmt.Printf("testVTabCursor.Filter: %v\n", vc)
	vc.index = 0
	return nil
}
func (vc *testVTabCursor) Next() error {
	//fmt.Printf("testVTabCursor.Next: %v\n", vc)
	vc.index++
	return nil
}
func (vc *testVTabCursor) Eof() bool {
	//fmt.Printf("testVTabCursor.Eof: %v\n", vc)
	return vc.index >= len(vc.vTab.intarray)
}
func (vc *testVTabCursor) Column(c *Context, col int) error {
	//fmt.Printf("testVTabCursor.Column(%d): %v\n", col, vc)
	if col != 0 {
		return fmt.Errorf("Column index out of bounds: %d", col)
	}
	c.ResultInt(vc.vTab.intarray[vc.index])
	return nil
}
func (vc *testVTabCursor) Rowid() (int64, error) {
	//fmt.Printf("testVTabCursor.Rowid: %v\n", vc)
	return int64(vc.index), nil
}

func TestCreateModule(t *testing.T) {
	db := open(t)
	defer checkClose(db, t)
	intarray := []int{1, 2, 3}
	err := db.CreateModule("test", testModule{t, intarray})
	checkNoError(t, err, "couldn't create module: %s")
	err = db.Exec("CREATE VIRTUAL TABLE vtab USING test('1', 2, three)")
	checkNoError(t, err, "couldn't create virtual table: %s")

	s, err := db.Prepare("SELECT rowid, * FROM vtab")
	checkNoError(t, err, "couldn't select from virtual table: %s")
	defer checkFinalize(s, t)
	var i, value int
	err = s.Select(func(s *Stmt) (err error) {
		if err = s.Scan(&i, &value); err != nil {
			return
		}
		assertEquals(t, "Expected '%d' but got '%d'", intarray[i], value)
		return
	})
	checkNoError(t, err, "couldn't select from virtual table: %s")

	err = db.Exec("DROP TABLE vtab")
	checkNoError(t, err, "couldn't drop virtual table: %s")
}
