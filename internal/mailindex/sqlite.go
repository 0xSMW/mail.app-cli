//go:build cgo && (darwin || linux)

// Package mailindex observes Mail's Envelope Index without opening a writable
// SQLite connection. Connections are reused, but every query releases its read
// transaction so a later observation can see Mail's committed changes.
package mailindex

/*
#cgo LDFLAGS: -lsqlite3
#include <sqlite3.h>
#include <stdlib.h>
#include <string.h>
#include <stdatomic.h>

// Keep cancellation visible to the VM even if interrupt arrives before its
// first step, when sqlite3_interrupt alone would be a no-op.
typedef struct { atomic_int canceled; } query_cancellation;
static query_cancellation *new_cancellation(void) {
    query_cancellation *state = calloc(1, sizeof(query_cancellation));
    if (state) atomic_init(&state->canceled, 0);
    return state;
}
static int cancellation_progress(void *state) {
    return atomic_load(&((query_cancellation *)state)->canceled);
}
static void install_cancellation(sqlite3 *db, query_cancellation *state) {
    sqlite3_progress_handler(db, 1000, cancellation_progress, state);
}
static void cancel_query(sqlite3 *db, query_cancellation *state) {
    atomic_store(&state->canceled, 1);
    sqlite3_interrupt(db);
}
static void clear_cancellation(sqlite3 *db) { sqlite3_progress_handler(db, 0, NULL, NULL); }

static int read_authorizer(void *unused, int action, const char *a, const char *b, const char *db, const char *trigger) {
    switch (action) {
        case SQLITE_SELECT:
        case SQLITE_READ:
        case SQLITE_RECURSIVE:
            return SQLITE_OK;
        case SQLITE_FUNCTION:
            if (b && (!sqlite3_stricmp(b, "load_extension") || !sqlite3_stricmp(b, "writefile"))) return SQLITE_DENY;
            return SQLITE_OK;
        default:
            return SQLITE_DENY;
    }
}
static void observe_only(sqlite3 *db, int enabled) {
    sqlite3_set_authorizer(db, enabled ? read_authorizer : NULL, NULL);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type connection struct {
	db     *C.sqlite3
	file   os.FileInfo
	closed bool // protected by gate
	gate   chan struct{}
	pool   *connectionPool
}

const maxConnectionsPerPath = 4

type connectionPool struct {
	mu      sync.Mutex
	readers []*connection
	changed chan struct{}
	retired bool
}

var connections = struct {
	sync.Mutex
	byPath map[string]*connectionPool
}{byPath: make(map[string]*connectionPool)}

func Available() bool { return true }

// Query executes one SELECT (including WITH) on a read-only connection and
// returns sqlite3 -json compatible rows. SQL that can mutate the database or
// connection, including PRAGMA, ATTACH, and transaction control, is rejected.
func Query(ctx context.Context, path, sql string) ([]byte, error) {
	queryCount.Add(1)
	started := time.Now()
	defer func() { queryNanoseconds.Add(int64(time.Since(started))) }()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return nil, fmt.Errorf("Envelope Index path must be absolute")
	}
	if strings.IndexByte(sql, 0) >= 0 {
		return nil, fmt.Errorf("query contains a NUL byte")
	}
	conn, err := acquire(ctx, path)
	if err != nil {
		return nil, err
	}
	defer release(conn)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := execInternal(conn.db, "BEGIN"); err != nil {
		return nil, err
	}
	defer func() { C.observe_only(conn.db, 0); _ = execInternal(conn.db, "ROLLBACK") }()
	C.observe_only(conn.db, 1)
	cancellation, err := newCancellation(conn.db)
	if err != nil {
		return nil, err
	}
	defer cancellation.close()

	// Join the watcher before releasing the gate; a delayed interrupt must
	// never cancel the next caller's observation on this connection.
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			cancellation.cancel()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-joined }()

	query := C.CString(sql)
	defer C.free(unsafe.Pointer(query))
	var stmt *C.sqlite3_stmt
	var tail *C.char
	if rc := C.sqlite3_prepare_v2(conn.db, query, -1, &stmt, &tail); rc != C.SQLITE_OK {
		return nil, queryError(ctx, conn.db, rc)
	}
	if stmt == nil {
		return []byte("[]"), nil
	}
	defer C.sqlite3_finalize(stmt)
	if C.sqlite3_stmt_readonly(stmt) == 0 {
		return nil, fmt.Errorf("Envelope Index query must be read-only")
	}
	// Prepare all trailing text before stepping, rejecting a second statement
	// even when its first statement was a harmless SELECT.
	for tail != nil && *tail != 0 {
		var extra *C.sqlite3_stmt
		var next *C.char
		rc := C.sqlite3_prepare_v2(conn.db, tail, -1, &extra, &next)
		if extra != nil {
			C.sqlite3_finalize(extra)
		}
		if rc != C.SQLITE_OK {
			return nil, queryError(ctx, conn.db, rc)
		}
		if extra != nil {
			return nil, fmt.Errorf("Envelope Index query must contain one read-only statement")
		}
		tail = next
	}
	rows := make([]map[string]any, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			break
		}
		if rc != C.SQLITE_ROW {
			return nil, queryError(ctx, conn.db, rc)
		}
		row := make(map[string]any)
		for i := C.int(0); i < C.sqlite3_column_count(stmt); i++ {
			name := C.GoString(C.sqlite3_column_name(stmt, i))
			switch C.sqlite3_column_type(stmt, i) {
			case C.SQLITE_INTEGER:
				row[name] = int64(C.sqlite3_column_int64(stmt, i))
			case C.SQLITE_FLOAT:
				row[name] = float64(C.sqlite3_column_double(stmt, i))
			case C.SQLITE_NULL:
				row[name] = nil
			default:
				value := C.sqlite3_column_text(stmt, i)
				row[name] = C.GoStringN((*C.char)(unsafe.Pointer(value)), C.sqlite3_column_bytes(stmt, i))
			}
		}
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(rows)
}

type queryCancellation struct {
	db    *C.sqlite3
	state *C.query_cancellation
}

func newCancellation(db *C.sqlite3) (*queryCancellation, error) {
	state := C.new_cancellation()
	if state == nil {
		return nil, fmt.Errorf("cannot allocate Envelope Index cancellation state")
	}
	C.install_cancellation(db, state)
	return &queryCancellation{db: db, state: state}, nil
}
func (c *queryCancellation) cancel() { C.cancel_query(c.db, c.state) }

// Only the exclusive lease owner clears the handler, after its watcher joins.
func (c *queryCancellation) close() { C.clear_cancellation(c.db); C.free(unsafe.Pointer(c.state)) }

func queryError(ctx context.Context, db *C.sqlite3, rc C.int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("Envelope Index query failed (%d): %s", int(rc), C.GoString(C.sqlite3_errmsg(db)))
}

func execInternal(db *C.sqlite3, sql string) error {
	value := C.CString(sql)
	defer C.free(unsafe.Pointer(value))
	if rc := C.sqlite3_exec(db, value, nil, nil, nil); rc != C.SQLITE_OK {
		return fmt.Errorf("Envelope Index observation failed (%d): %s", int(rc), C.GoString(C.sqlite3_errmsg(db)))
	}
	return nil
}

// A lease exclusively owns one connection until statement finalization,
// watcher joining and transaction rollback have all finished. Idle readers are
// reused before another is opened; parallel callers can open at most four.
func acquire(ctx context.Context, path string) (*connection, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		connections.Lock()
		pool := connections.byPath[path]
		if pool == nil {
			pool = &connectionPool{changed: make(chan struct{})}
			connections.byPath[path] = pool
		}
		connections.Unlock()
		pool.mu.Lock()
		if pool.retired {
			pool.mu.Unlock()
			continue
		}
		var conn *connection
		for _, candidate := range pool.readers {
			select {
			case <-candidate.gate:
				conn = candidate
			default:
			}
			if conn != nil {
				break
			}
		}
		if conn == nil && len(pool.readers) < maxConnectionsPerPath {
			conn = &connection{gate: make(chan struct{}, 1), pool: pool}
			pool.readers = append(pool.readers, conn)
		}
		wake := pool.changed
		pool.mu.Unlock()
		if conn == nil {
			select {
			case <-wake:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			release(conn)
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			release(conn)
			return nil, fmt.Errorf("open Envelope Index: %w", err)
		}
		if !info.Mode().IsRegular() {
			release(conn)
			return nil, fmt.Errorf("Envelope Index is not a regular file")
		}
		if conn.db != nil && os.SameFile(info, conn.file) {
			return conn, nil
		}
		if conn.db != nil {
			C.sqlite3_close_v2(conn.db)
			conn.db = nil
		}
		if err := openConnection(conn, path, info); err != nil {
			release(conn)
			return nil, err
		}
		return conn, nil
	}
}

func release(conn *connection) {
	conn.gate <- struct{}{}
	conn.pool.mu.Lock()
	close(conn.pool.changed)
	conn.pool.changed = make(chan struct{})
	conn.pool.mu.Unlock()
}

func openConnection(conn *connection, path string, info os.FileInfo) error {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	var db *C.sqlite3
	rc := C.sqlite3_open_v2(name, &db, C.SQLITE_OPEN_READONLY|C.SQLITE_OPEN_FULLMUTEX, nil)
	if rc != C.SQLITE_OK {
		message := C.GoString(C.sqlite3_errmsg(db))
		if db != nil {
			C.sqlite3_close_v2(db)
		}
		return fmt.Errorf("open Envelope Index (%d): %s", int(rc), message)
	}
	C.sqlite3_busy_timeout(db, 250)
	// Defense in depth: READONLY and the SQL authorizer already exclude writes.
	if err := execInternal(db, "PRAGMA query_only=ON"); err != nil {
		C.sqlite3_close_v2(db)
		return err
	}
	conn.db, conn.file = db, info
	connectionOpens.Add(1)
	return nil
}

// Close releases existing process-lifetime connections after in-flight
// observations. Later calls can open a fresh connection, including concurrent
// callers waiting for an observation that Close just retired.
func Close() {
	connections.Lock()
	retired := connections.byPath
	connections.byPath = make(map[string]*connectionPool)
	// Retire under the registry lock so no new caller can lease an old reader.
	for _, pool := range retired {
		pool.mu.Lock()
		pool.retired = true
		close(pool.changed)
		pool.changed = make(chan struct{})
		pool.mu.Unlock()
	}
	connections.Unlock()
	for _, pool := range retired {
		for _, conn := range pool.readers {
			<-conn.gate
			if conn.db != nil {
				C.sqlite3_close_v2(conn.db)
				conn.db = nil
			}
			conn.closed = true
			release(conn)
		}
	}
}

// Reset is useful when a test changes its isolated index fixture.
func Reset() { Close() }
