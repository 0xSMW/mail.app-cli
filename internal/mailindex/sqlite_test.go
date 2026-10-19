//go:build cgo && (darwin || linux)

package mailindex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "邮件 café", "Envelope Index")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, path, "create table observations(value text); insert into observations values ('héllo 😀');")
	t.Cleanup(Reset)
	return path
}

func fixtureWrite(t *testing.T, path, sql string) {
	t.Helper()
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("fixture setup requires sqlite3")
	}
	if output, err := exec.Command(binary, path, sql).CombinedOutput(); err != nil {
		t.Fatalf("fixture setup: %v: %s", err, output)
	}
}

func TestQueryReadOnlyAndJSON(t *testing.T) {
	path := fixtureDB(t)
	output, err := Query(context.Background(), path, "select value, 42 as integerValue, 2.5 as floatValue, NULL as empty from observations;")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(output, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["value"] != "héllo 😀" || rows[0]["integerValue"] != float64(42) || rows[0]["floatValue"] != 2.5 || rows[0]["empty"] != nil {
		t.Fatalf("rows = %#v", rows)
	}
	for _, sql := range []string{
		"insert into observations values ('bad');",
		"delete from observations;",
		"drop table observations;",
		"pragma query_only=off;",
		"pragma user_version=99;",
		"begin;",
		"attach ':memory:' as extra;",
		"select value from observations; delete from observations;",
		"select load_extension('absent');",
	} {
		if result, err := Query(context.Background(), path, sql); err == nil {
			t.Fatalf("accepted mutating SQL %q: %s", sql, result)
		}
	}
	output, err = Query(context.Background(), path, "select count(*) as n from observations;")
	if err != nil || string(output) != `[{"n":1}]` {
		t.Fatalf("mutating query changed fixture: %s, %v", output, err)
	}
}

func TestConnectionReusedWithFreshObservations(t *testing.T) {
	path := fixtureDB(t)
	if _, err := Query(context.Background(), path, "select * from observations;"); err != nil {
		t.Fatal(err)
	}
	connections.Lock()
	first := connections.byPath[path]
	connections.Unlock()
	fixtureWrite(t, path, "insert into observations values ('new committed value');")
	output, err := Query(context.Background(), path, "select count(*) as n from observations;")
	if err != nil || string(output) != `[{"n":2}]` {
		t.Fatalf("stale observation: %s, %v", output, err)
	}
	connections.Lock()
	second := connections.byPath[path]
	connections.Unlock()
	if first != second {
		t.Fatal("connection was not reused")
	}
	Close()
	output, err = Query(context.Background(), path, "select count(*) as n from observations;")
	if err != nil || string(output) != `[{"n":2}]` {
		t.Fatalf("query after Close: %s, %v", output, err)
	}
}

func TestCancellationInterruptDoesNotLeakToNextQuery(t *testing.T) {
	path := fixtureDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Query(ctx, path, "with recursive n(x) as (select 1 union all select x+1 from n where x < 1000000000) select sum(x) from n;")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancellation took %s", time.Since(started))
	}
	for i := 0; i < 10; i++ {
		output, err := Query(context.Background(), path, "select 1 as ok;")
		if err != nil || string(output) != `[{"ok":1}]` {
			t.Fatalf("interrupt leaked to next query: %s, %v", output, err)
		}
	}
}

func TestQueuedQueryCancellation(t *testing.T) {
	path := fixtureDB(t)
	var held []*connection
	for i := 0; i < maxConnectionsPerPath; i++ {
		conn, err := acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	defer func() {
		for _, conn := range held {
			release(conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Query(ctx, path, "select 1;")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation = %v", err)
	}
	// A query waiting for one path must not hold the registry and prevent a
	// different fixture from opening its own connection.
	other := fixtureDB(t)
	if _, err := Query(context.Background(), other, "select 1;"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingDatabaseAndMalformedQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := Query(context.Background(), path, "select 1;"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only open created a database")
	}
	path = fixtureDB(t)
	if _, err := Query(context.Background(), path, "select missing_column from observations;"); err == nil {
		t.Fatal("schema error accepted")
	}
	if _, err := Query(context.Background(), path, "select 1;"); err != nil {
		t.Fatalf("schema error poisoned connection: %v", err)
	}
}

func TestPoolReusesIdleReadersAndBoundsParallelLeases(t *testing.T) {
	path := fixtureDB(t)
	before := Statistics().ConnectionOpens
	first, err := acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	release(first)
	for i := 0; i < 10; i++ {
		conn, err := acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if conn != first {
			release(conn)
			t.Fatal("sequential lease did not reuse the idle reader")
		}
		release(conn)
	}
	if Statistics().ConnectionOpens-before != 1 {
		t.Fatal("sequential observations opened extra readers")
	}
	var held []*connection
	for i := 0; i < maxConnectionsPerPath; i++ {
		conn, err := acquire(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		for _, other := range held {
			if conn == other {
				t.Fatal("simultaneous leases share a connection")
			}
		}
		held = append(held, conn)
	}
	for _, conn := range held {
		release(conn)
	}
	if Statistics().ConnectionOpens-before != maxConnectionsPerPath {
		t.Fatal("parallel pool did not respect its connection bound")
	}
	fixtureWrite(t, path, "insert into observations values ('fresh');")
	for _, expected := range held {
		// Hold all other readers so this observation must reuse the chosen one.
		var blockers []*connection
		for _, conn := range held {
			if conn != expected {
				<-conn.gate
				blockers = append(blockers, conn)
			}
		}
		out, err := Query(context.Background(), path, "select count(*) as n from observations;")
		for _, conn := range blockers {
			release(conn)
		}
		if err != nil || string(out) != `[{"n":2}]` {
			t.Fatalf("reader retained a stale snapshot: %s %v", out, err)
		}
	}
}

func TestCancellationDoesNotInterruptAnotherReader(t *testing.T) {
	path := fixtureDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	longResult := make(chan error, 1)
	go func() {
		_, err := Query(ctx, path, "with recursive n(x) as (select 1 union all select x+1 from n where x < 1000000000) select sum(x) from n;")
		longResult <- err
	}()
	type outcome struct {
		data []byte
		err  error
	}
	neighborResult := make(chan outcome, 1)
	go func() {
		data, err := Query(context.Background(), path, "with recursive n(x) as (select 1 union all select x+1 from n where x < 1000000) select sum(x) as total from n;")
		neighborResult <- outcome{data, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		connections.Lock()
		pool := connections.byPath[path]
		connections.Unlock()
		leased := 0
		if pool != nil {
			pool.mu.Lock()
			for _, conn := range pool.readers {
				if len(conn.gate) == 0 {
					leased++
				}
			}
			pool.mu.Unlock()
		}
		if leased == 2 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("queries never owned distinct simultaneous readers")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-longResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	other := <-neighborResult
	if other.err != nil || string(other.data) != `[{"total":500000500000}]` {
		t.Fatalf("neighbor interrupted: %s %v", other.data, other.err)
	}
	for i := 0; i < 10; i++ {
		if _, err := Query(context.Background(), path, "select 1;"); err != nil {
			t.Fatalf("interrupt leaked to a reused reader: %v", err)
		}
	}
}

func TestCancellationBeforeFirstStepRemainsVisibleToVM(t *testing.T) {
	path := fixtureDB(t)
	conn, err := acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	flag, err := newCancellation(conn.db)
	if err != nil {
		release(conn)
		t.Fatal(err)
	}
	flag.cancel() // No active statement exists yet: interrupt alone is a no-op.
	err = execInternal(conn.db, "with recursive n(x) as (select 1 union all select x+1 from n where x < 1000000000) select sum(x) from n;")
	flag.close()
	release(conn)
	if err == nil {
		t.Fatal("cancellation before first step was lost")
	}
	if out, err := Query(context.Background(), path, "select 1 as ok;"); err != nil || string(out) != `[{"ok":1}]` {
		t.Fatalf("canceled handler leaked to next lease: %s %v", out, err)
	}
}
