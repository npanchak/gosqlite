// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite_test

import (
	"fmt"
	. "github.com/gwenn/gosqlite"
	"testing"
)

func init() {
	err := ConfigThreadingMode(Serialized)
	if err != nil {
		panic(fmt.Sprintf("cannot change threading mode: '%s'", err))
	}
	err = ConfigMemStatus(true)
	if err != nil {
		panic(fmt.Sprintf("cannot activate mem status: '%s'", err))
	}
	err = ConfigUri(true)
	if err != nil {
		panic(fmt.Sprintf("cannot activate uri handling: '%s'", err))
	}
	err = ConfigLog(func(d interface{}, err error, msg string) {
		fmt.Printf("%s: %s, %s\n", d, err, msg)
	}, "SQLITE")
	if err != nil {
		panic(fmt.Sprintf("couldn't config log: '%s'", err))
	}
	err = ConfigLog(nil, "")
	if err != nil {
		panic(fmt.Sprintf("couldn't unset logger: '%s'", err))
	}
	err = EnableSharedCache(false)
	if err != nil {
		panic(fmt.Sprintf("couldn't disable shared cache: '%s'", err))
	}
}

func trace(d interface{}, sql string) {
	if t, ok := d.(*testing.T); ok {
		t.Logf("TRACE: %s\n", sql)
	} else {
		fmt.Printf("%s: %s\n", d, sql)
	}
}

func authorizer(d interface{}, action Action, arg1, arg2, dbName, triggerName string) Auth {
	if t, ok := d.(*testing.T); ok {
		t.Logf("AUTH: %s, %s, %s, %s, %s\n", action, arg1, arg2, dbName, triggerName)
	} else {
		fmt.Printf("%s: %s, %s, %s, %s, %s\n", d, action, arg1, arg2, dbName, triggerName)
	}
	return AuthOk
}

func profile(d interface{}, sql string, nanoseconds uint64) {
	if t, ok := d.(*testing.T); ok {
		t.Logf("PROFILE: %s = %d µs\n", sql, nanoseconds/1e3)
	} else {
		fmt.Printf("%s: %s = %d µs\n", d, sql, nanoseconds/1e3)
	}
}

func progressHandler(d interface{}) bool {
	if t, ok := d.(*testing.T); ok {
		t.Log("+")
	} else {
		fmt.Print("+")
	}
	return false
}

func commitHook(d interface{}) bool {
	if t, ok := d.(*testing.T); ok {
		t.Log("CMT")
	} else {
		fmt.Println(d)
	}
	return false
}

func rollbackHook(d interface{}) {
	if t, ok := d.(*testing.T); ok {
		t.Log("RBK")
	} else {
		fmt.Println(d)
	}
}

func updateHook(d interface{}, a Action, dbName, tableName string, rowId int64) {
	if t, ok := d.(*testing.T); ok {
		t.Logf("UPD: %d, %s.%s.%d\n", a, dbName, tableName, rowId)
	} else {
		fmt.Printf("%s: %d, %s.%s.%d\n", d, a, dbName, tableName, rowId)
	}
}

func TestNoTrace(t *testing.T) {
	db := open(t)
	defer checkClose(db, t)
	db.Trace(nil, nil)
	db.SetAuthorizer(nil, nil)
	db.Profile(nil, nil)
	db.ProgressHandler(nil, 0, nil)
	db.BusyHandler(nil, nil)
	db.CommitHook(nil, nil)
	db.RollbackHook(nil, nil)
	db.UpdateHook(nil, nil)
}

func TestTrace(t *testing.T) {
	db := open(t)
	defer checkClose(db, t)
	db.Trace(trace, t)
	err := db.SetAuthorizer(authorizer, t)
	checkNoError(t, err, "couldn't set an authorizer")
	db.Profile(profile, t)
	db.ProgressHandler(progressHandler, 1, t)
	db.CommitHook(commitHook, t)
	db.RollbackHook(rollbackHook, t)
	db.UpdateHook(updateHook, t)
	db.Exists("SELECT 1 WHERE 1 = ?", 1)
}

func TestLog(t *testing.T) {
	Log(0, "One message")
}

func TestMemory(t *testing.T) {
	used := MemoryUsed()
	assert(t, "memory used", used >= 0)
	highwater := MemoryHighwater(false)
	assert(t, "memory highwater", highwater >= 0)
	limit := SoftHeapLimit()
	assert(t, "soft heap limit positive", limit >= 0)
}
