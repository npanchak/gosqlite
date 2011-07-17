package sqlite

import (
	"testing"
	"os"
)

func open(t *testing.T) *Conn {
	db, err := Open("")
	if err != nil {
		t.Fatalf("couldn't open database file: %s", err)
	}
	if db == nil {
		t.Fatal("opened database is nil")
	}
	return db
}

func createTable(db *Conn, t *testing.T) {
	err := db.Exec("DROP TABLE IF EXISTS test;" +
		"CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT," +
		" float_num REAL, int_num INTEGER, a_string TEXT); -- bim")
	if err != nil {
		t.Fatalf("error creating table: %s", err)
	}
}

func TestOpen(t *testing.T) {
	db := open(t)
	db.Close()
}

func TestCreateTable(t *testing.T) {
	db := open(t)
	defer db.Close()
	createTable(db, t)
}

func TestInsert(t *testing.T) {
	db := open(t)
	defer db.Close()
	createTable(db, t)
	for i := 0; i < 1000; i++ {
		ierr := db.Exec("INSERT INTO test (float_num, int_num, a_string) VALUES (?, ?, ?)", float64(i)*float64(3.14), i, "hello")
		if ierr != nil {
			t.Fatalf("insert error: %s", ierr)
		}
		c := db.Changes()
		if c != 1 {
			t.Errorf("insert error: %d <> 1", c)
		}
	}

	cs, _ := db.Prepare("SELECT COUNT(*) FROM test")
	defer cs.Finalize()
	if ok, err := cs.Next(); !ok {
		if err != nil {
			t.Fatalf("error preparing count: %s", err)
		}
		t.Fatal("no result for count")
	}
	var i int
	err := cs.Scan(&i)
	if err != nil {
		t.Fatalf("error scanning count: %s", err)
	}
	if i != 1000 {
		t.Errorf("count should be 1000, but it is %d", i)
	}
}

func TestInsertWithStatement(t *testing.T) {
	db := open(t)
	defer db.Close()
	createTable(db, t)
	s, serr := db.Prepare("INSERT INTO test (float_num, int_num, a_string) VALUES (?, ?, ?)")
	if serr != nil {
		t.Fatalf("prepare error: %s", serr)
	}
	if s == nil {
		t.Fatal("statement is nil")
	}
	defer s.Finalize()

	for i := 0; i < 1000; i++ {
		ierr := s.Exec(float64(i)*float64(3.14), i, "hello")
		if ierr != nil {
			t.Fatalf("insert error: %s", ierr)
		}
		c := db.Changes()
		if c != 1 {
			t.Errorf("insert error: %d <> 1", c)
		}
	}

	cs, _ := db.Prepare("SELECT COUNT(*) FROM test")
	defer cs.Finalize()
	if ok, _ := cs.Next(); !ok {
		t.Fatal("no result for count")
	}
	var i int
	err := cs.Scan(&i)
	if err != nil {
		t.Fatalf("error scanning count: %s", err)
	}
	if i != 1000 {
		t.Errorf("count should be 1000, but it is %d", i)
	}

	rs, _ := db.Prepare("SELECT float_num, int_num, a_string FROM test ORDER BY int_num LIMIT 2")
	var fnum float64
	var inum int64
	var sstr string
	if ok, _ := rs.Next(); ok {
		rs.Scan(&fnum, &inum, &sstr)
		if fnum != 0 {
			t.Errorf("Expected 0 <> %f\n", fnum)
		}
		if inum != 0 {
			t.Errorf("Expected 0 <> %d\n", inum)
		}
		if sstr != "hello" {
			t.Errorf("Expected 'hello' <> %s\n", sstr)
		}
	}
	if ok, _ := rs.Next(); ok {
		var fnum float64
		var inum int64
		var sstr string
		rs.NamedScan("a_string", &sstr, "float_num", &fnum, "int_num", &inum)
		if fnum != 3.14 {
			t.Errorf("Expected 3.14 <> %f\n", fnum)
		}
		if inum != 1 {
			t.Errorf("Expected 1 <> %d\n", inum)
		}
		if sstr != "hello" {
			t.Errorf("Expected 'hello' <> %s\n", sstr)
		}
	}
}

func BenchmarkScan(b *testing.B) {
	b.StopTimer()
	db, _ := Open("")
	defer db.Close()
	db.Exec("DROP TABLE IF EXISTS test")
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, float_num REAL, int_num INTEGER, a_string TEXT)")
	s, _ := db.Prepare("INSERT INTO test (float_num, int_num, a_string) VALUES (?, ?, ?)")

	for i := 0; i < 1000; i++ {
		s.Exec(float64(i)*float64(3.14), i, "hello")
	}
	s.Finalize()

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		cs, _ := db.Prepare("SELECT float_num, int_num, a_string FROM test")

		var fnum float64
		var inum int64
		var sstr string

		var ok bool
		var err os.Error
		for ok, err = cs.Next(); ok; ok, err = cs.Next() {
			cs.Scan(&fnum, &inum, &sstr)
		}
		if err != nil {
			panic(err)
		}
		cs.Finalize()
	}
}

func BenchmarkNamedScan(b *testing.B) {
	b.StopTimer()
	db, _ := Open("")
	defer db.Close()
	db.Exec("DROP TABLE IF EXISTS test")
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT, float_num REAL, int_num INTEGER, a_string TEXT)")
	s, _ := db.Prepare("INSERT INTO test (float_num, int_num, a_string) VALUES (?, ?, ?)")

	for i := 0; i < 1000; i++ {
		s.Exec(float64(i)*float64(3.14), i, "hello")
	}
	s.Finalize()

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		cs, _ := db.Prepare("SELECT float_num, int_num, a_string FROM test")

		var fnum float64
		var inum int64
		var sstr string

		var ok bool
		var err os.Error
		for ok, err = cs.Next(); ok; ok, err = cs.Next() {
			cs.NamedScan("float_num", &fnum, "int_num", &inum, "a_string", &sstr)
		}
		if err != nil {
			panic(err)
		}
		cs.Finalize()
	}
}

func BenchmarkInsert(b *testing.B) {
	db, _ := Open("")
	defer db.Close()
	db.Exec("DROP TABLE IF EXISTS test")
	db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY AUTOINCREMENT," +
		" float_num REAL, int_num INTEGER, a_string TEXT)")
	s, _ := db.Prepare("INSERT INTO test (float_num, int_num, a_string)" +
		" VALUES (?, ?, ?)")
	defer s.Finalize()

	for i := 0; i < b.N; i++ {
		s.Exec(float64(i)*float64(3.14), i, "hello")
	}
}