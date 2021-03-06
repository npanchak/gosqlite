Yet another SQLite binding based on:
 - original [Russ Cox's](http://code.google.com/p/gosqlite/) implementation,
 - the [Patrick Crosby's](https://github.com/patrickxb/fgosqlite/) fork.

This binding implements the "database/sql/driver" interface.

See [package documentation](http://godoc.org/github.com/gwenn/gosqlite).

[![Build Status][1]][2]

[1]: https://secure.travis-ci.org/gwenn/gosqlite.png
[2]: http://www.travis-ci.org/gwenn/gosqlite

### Features (not supported by database/sql/driver):

* Dynamic type: currently, the SQLite3 manifest typing is respected. There is no use of the column declared type to guess the target/go type when scanning. On your side, you should try to not break column affinity rules (such as declaring a column with TIMESTAMP type (NUMERIC affinity) storing values with '2006-01-02T15:04:05.999Z07:00' format (TEXT type))...
* Partial scan: scan values may be partially scanned (by index or name) or skipped/ignored by passing nil pointer(s).
* Null value: by default, empty string and zero time are bound to null for prepared statement's parameters (no need for NullString, NullTime but still supported).

### Changes:

Open supports flags.  
Conn.Exec handles multiple statements (separated by semicolons) properly.  
Conn.Prepare can optionnaly bind as well.  
Conn.Prepare can reuse already prepared Stmt.  
Conn.Close ensures that all dangling statements are finalized.  
Stmt.Exec is renamed in Stmt.Bind and a new Stmt.Exec method is introduced to bind and step.  
Stmt.Bind uses native sqlite3_bind_x methods and failed if unsupported type.  
Stmt.NamedBind can be used to bind by name.  
Stmt.Next returns a (bool, os.Error) couple like Reader.Read.  
Stmt.Scan uses native sqlite3_column_x methods.  
Stmt.NamedScan is added. It's compliant with [go-dbi](https://github.com/thomaslee/go-dbi/).  
Stmt.ScanByIndex/ScanByName are added to test NULL value.

Currently, the weak point of the binding is the *Scan* methods:
The original implementation is using this strategy:
 - convert the stored value to a []byte by calling sqlite3_column_blob,
 - convert the bytes to the desired Go type with correct feedback in case of illegal conversion,
 - but apparently no support for NULL value.

Using the native sqlite3_column_x implies:
 - optimal conversion from the storage type to Go type (when they match),
 - loosy conversion when types mismatch (select cast('M' as int); --> 0),
 - NULL value can be returned only for **type, otherwise a default value (0, false, "") is returned.

SQLite logs (SQLITE_CONFIG_LOG) can be activated by:
- ConfigLog function
- or `export SQLITE_LOG=1`

### Similar projects created after Jul 17, 2011:

https://github.com/mattn/go-sqlite3 (Nov 11, 2011)  
https://code.google.com/p/go-sqlite (Feb 12, 2013)  

### Additions:

Conn.Exists  
Conn.OneValue  

Conn.OpenVfs  
Conn.EnableFkey/IsFKeyEnabled  
Conn.Changes/TotalChanges  
Conn.LastInsertRowid  
Conn.Interrupt  
Conn.Begin/BeginTransaction(type)/Commit/Rollback  
Conn.GetAutocommit  
Conn.EnableLoadExtension/LoadExtension  
Conn.IntegrityCheck  

Stmt.Insert/ExecDml/Select/SelectOneRow  
Stmt.BindParameterCount/BindParameterIndex(name)/BindParameterName(index)  
Stmt.ClearBindings  
Stmt.ColumnCount/ColumnNames/ColumnIndex(name)/ColumnName(index)/ColumnType(index)  
Stmt.ReadOnly  
Stmt.Busy  

Blob:  
ZeroBlobLength  
Conn.NewBlobReader  
Conn.NewBlobReadWriter  

Meta:  
Conn.Databases  
Conn.Tables  
Conn.Columns  
Conn.ForeignKeys  
Conn.Indexes/IndexColumns  

Time:  
JulianDay  
JulianDayToUTC  
JulianDayToLocalTime  
UnixTime, JulianTime and TimeStamp used to persist go time in formats supported by SQLite3 date functions.

Trace:  
Conn.BusyHandler  
Conn.Profile  
Conn.ProgressHandler  
Conn.SetAuthorizer  
Conn.Trace  
Stmt.Status  

Hook:  
Conn.CommitHook  
Conn.RollbackHook  
Conn.UpdateHook  

Function:  
Conn.CreateScalarFunction  
Conn.CreateAggregateFunction  

Virtual Table (partial support):  
Conn.CreateModule  
Conn.DeclareVTab  

### GC:
Although Go is gced, there is no destructor (see http://www.airs.com/blog/archives/362).  
In the gosqlite wrapper, no finalizer is used.  
So users must ensure that C ressources (database connections, prepared statements, BLOBs, Backups) are destroyed/deallocated by calling Conn.Close, Stmt.Finalize, BlobReader.Close, Backup.Close.

Therefore, sqlite3_close/sqlite3_next_stmt are used by Conn.Close to free the database connection and all dangling statements (not sqlite3_close_v2) (see http://sqlite.org/c3ref/close.html).

### Benchmarks:
$ go test -bench . -benchmem
<pre>
BenchmarkValuesScan	  500000	      6265 ns/op	      74 B/op	       3 allocs/op
BenchmarkScan	  500000	      4994 ns/op	      41 B/op	       4 allocs/op
BenchmarkNamedScan	  500000	      4960 ns/op	      93 B/op	       7 allocs/op

BenchmarkInsert	  500000	      4085 ns/op	      16 B/op	       1 allocs/op
BenchmarkNamedInsert	  500000	      4798 ns/op	      64 B/op	       4 allocs/op

BenchmarkDisabledCache	  100000	     19841 ns/op	     117 B/op	       3 allocs/op
BenchmarkEnabledCache	 2000000	       790 ns/op	      50 B/op	       1 allocs/op

BenchmarkLike	 1000000	      2605 ns/op	       0 B/op	       0 allocs/op
BenchmarkHalf	  500000	      4988 ns/op	      33 B/op	       1 allocs/op
BenchmarkRegexp	  500000	      5557 ns/op	       8 B/op	       1 allocs/op
</pre>
