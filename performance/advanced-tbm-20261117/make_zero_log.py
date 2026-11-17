"""Isolated initialized-extent experiment; never edits the baseline."""
from pathlib import Path
import shutil

root=Path('/home/platemo/seekdb-experiments/20261117/experiment-runtime')
dest=root/'advanced-candidates/zerolog'
shutil.copytree(root/'remote-final',dest)
p=dest/'internal/storage/log.go'
s=p.read_text().replace('stats   Stats','stats   Stats\n\tphysical int64 // initialized physical extent, possibly past logical EOF')
s=s.replace('synced: st.Size(), mode: mode,','synced: st.Size(), mode: mode, physical: st.Size(),')
s=s.replace('if _, err := l.f.WriteAt(l.buf, l.flushed); err != nil {','if err := l.initializeThrough(l.flushed + int64(len(l.buf))); err != nil {\n\t\treturn err\n\t}\n\tif _, err := l.f.WriteAt(l.buf, l.flushed); err != nil {')
s=s.replace('l.size, l.flushed, l.synced = n, n, n','l.size, l.flushed, l.synced, l.physical = n, n, n, n')
p.write_text(s)
(dest/'internal/storage/initialize.go').write_text('''package storage

// The extra zero suffix is not a record and is never exposed by Size, Map,
// or Scanner on the open Log. On reopen the existing CRC/commit recovery
// truncates it to the last complete committed transaction. Zero initialization
// and the real records are flushed by the SAME normal fdatasync before ack.
// Overwriting already initialized extents avoids allocating/extending the
// file for every small commit. Its write amplification is measured separately.
const initializedChunk = 1 << 20

var initializedZeros [64 << 10]byte

func (l *Log) initializeThrough(end int64) error {
	if end <= l.physical {
		return nil
	}
	target := (end + initializedChunk - 1) / initializedChunk * initializedChunk
	for l.physical < target {
		n := min(int64(len(initializedZeros)), target-l.physical)
		written, err := l.f.WriteAt(initializedZeros[:n], l.physical)
		l.physical += int64(written)
		l.stats.WriteCalls++
		l.stats.BytesWritten += int64(written)
		if err != nil {
			return err
		}
	}
	return nil
}
''')
(dest/'internal/storage/initialize_test.go').write_text('''package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInitializedExtentLogicalBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.log")
	l, err := OpenLog(path, SyncData)
	if err != nil { t.Fatal(err) }
	payload := bytes.Repeat([]byte{37}, 70003)
	for i := 0; i < 20; i++ {
		if _, err = l.Append(RecPut, uint64(i+1), payload); err != nil { t.Fatal(err) }
		if err = l.Sync(); err != nil { t.Fatal(err) }
		if l.Size() != int64((i+1)*(HdrSize+len(payload))) { t.Fatal("logical EOF changed") }
		if l.physical-l.Size() >= initializedChunk { t.Fatal("unbounded padding") }
	}
	l.Prepare()
	if int64(len(l.Map())) != l.Size() { t.Fatal("padding exposed in Map") }
	sc := NewScanner(l, 0)
	var r Record
	n := 0
	for sc.Next(&r) {
		if !bytes.Equal(r.Payload, payload) { t.Fatal("payload overwritten") }
		n++
	}
	if n != 20 || sc.Err != nil || sc.Good != l.Size() { t.Fatal("scan mismatch", n, sc.Err) }
	good := l.Size()
	if err = l.Close(); err != nil { t.Fatal(err) }
	st, err := os.Stat(path)
	if err != nil || st.Size() <= good { t.Fatal("no physical padding") }
	l, err = OpenLog(path, SyncData)
	if err != nil { t.Fatal(err) }
	sc = NewScanner(l, 0)
	n = 0
	for sc.Next(&r) { n++ }
	if n != 20 || sc.Err != nil || sc.Good != good { t.Fatal("zero tail accepted as record") }
	if err = l.Truncate(good); err != nil { t.Fatal(err) }
	if l.physical != good { t.Fatal("stale physical end after truncate") }
	if _, err = l.Append(RecCommit, 20, nil); err != nil { t.Fatal(err) }
	if err = l.Close(); err != nil { t.Fatal(err) }
}
''')
print(dest)
