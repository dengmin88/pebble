// Copyright 2020 The LevelDB-Go and Pebble Authors. All rights reserved. Use
// of this source code is governed by a BSD-style license that can be found in
// the LICENSE file.

package vfs

import (
	"io"
	"math"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/require"
)

type mockFile struct {
	syncAndWriteDuration time.Duration
}

func (m mockFile) Close() error {
	return nil
}

func (m mockFile) Read(p []byte) (n int, err error) {
	panic("unimplemented")
}

func (m mockFile) ReadAt(p []byte, off int64) (n int, err error) {
	panic("unimplemented")
}

func (m mockFile) Write(p []byte) (n int, err error) {
	time.Sleep(m.syncAndWriteDuration)
	return len(p), nil
}

func (m mockFile) Prefetch(offset, length int64) error {
	panic("unimplemented")
}

func (m mockFile) Preallocate(int64, int64) error {
	time.Sleep(m.syncAndWriteDuration)
	return nil
}

func (m mockFile) Stat() (os.FileInfo, error) {
	panic("unimplemented")
}

func (m mockFile) Fd() uintptr {
	return InvalidFd
}

func (m mockFile) Sync() error {
	time.Sleep(m.syncAndWriteDuration)
	return nil
}

func (m mockFile) SyncData() error {
	time.Sleep(m.syncAndWriteDuration)
	return nil
}

func (m mockFile) SyncTo(int64) (fullSync bool, err error) {
	time.Sleep(m.syncAndWriteDuration)
	return false, nil
}

var _ File = &mockFile{}

type mockFS struct {
	create        func(string) (File, error)
	link          func(string, string) error
	list          func(string) ([]string, error)
	lock          func(string) (io.Closer, error)
	mkdirAll      func(string, os.FileMode) error
	open          func(string, ...OpenOption) (File, error)
	openDir       func(string) (File, error)
	pathBase      func(string) string
	pathJoin      func(...string) string
	pathDir       func(string) string
	remove        func(string) error
	removeAll     func(string) error
	rename        func(string, string) error
	reuseForWrite func(string, string) (File, error)
	stat          func(string) (os.FileInfo, error)
	getDiskUsage  func(string) (DiskUsage, error)
}

func (m mockFS) Create(name string) (File, error) {
	if m.create == nil {
		panic("unimplemented")
	}
	return m.create(name)
}

func (m mockFS) Link(oldname, newname string) error {
	if m.link == nil {
		panic("unimplemented")
	}
	return m.link(oldname, newname)
}

func (m mockFS) Open(name string, opts ...OpenOption) (File, error) {
	if m.open == nil {
		panic("unimplemented")
	}
	return m.open(name, opts...)
}

func (m mockFS) OpenDir(name string) (File, error) {
	if m.openDir == nil {
		panic("unimplemented")
	}
	return m.openDir(name)
}

func (m mockFS) Remove(name string) error {
	if m.remove == nil {
		panic("unimplemented")
	}
	return m.remove(name)
}

func (m mockFS) RemoveAll(name string) error {
	if m.removeAll == nil {
		panic("unimplemented")
	}
	return m.removeAll(name)
}

func (m mockFS) Rename(oldname, newname string) error {
	if m.rename == nil {
		panic("unimplemented")
	}
	return m.rename(oldname, newname)
}

func (m mockFS) ReuseForWrite(oldname, newname string) (File, error) {
	if m.reuseForWrite == nil {
		panic("unimplemented")
	}
	return m.reuseForWrite(oldname, newname)
}

func (m mockFS) MkdirAll(dir string, perm os.FileMode) error {
	if m.mkdirAll == nil {
		panic("unimplemented")
	}
	return m.mkdirAll(dir, perm)
}

func (m mockFS) Lock(name string) (io.Closer, error) {
	if m.lock == nil {
		panic("unimplemented")
	}
	return m.lock(name)
}

func (m mockFS) List(dir string) ([]string, error) {
	if m.list == nil {
		panic("unimplemented")
	}
	return m.list(dir)
}

func (m mockFS) Stat(name string) (os.FileInfo, error) {
	if m.stat == nil {
		panic("unimplemented")
	}
	return m.stat(name)
}

func (m mockFS) PathBase(path string) string {
	if m.pathBase == nil {
		panic("unimplemented")
	}
	return m.pathBase(path)
}

func (m mockFS) PathJoin(elem ...string) string {
	if m.pathJoin == nil {
		panic("unimplemented")
	}
	return m.pathJoin(elem...)
}

func (m mockFS) PathDir(path string) string {
	if m.pathDir == nil {
		panic("unimplemented")
	}
	return m.pathDir(path)
}

func (m mockFS) GetDiskUsage(path string) (DiskUsage, error) {
	if m.getDiskUsage == nil {
		panic("unimplemented")
	}
	return m.getDiskUsage(path)
}

var _ FS = &mockFS{}

func TestDiskHealthChecking_File(t *testing.T) {
	oldTickInterval := defaultTickInterval
	defaultTickInterval = time.Millisecond
	defer func() { defaultTickInterval = oldTickInterval }()

	const (
		slowThreshold = 50 * time.Millisecond
	)

	fiveKB := make([]byte, 5*writeSizePrecision)
	testCases := []struct {
		op               OpType
		writeSize        int
		writeDuration    time.Duration
		fn               func(f File)
		createWriteDelta time.Duration
		expectStall      bool
	}{
		// No false negatives.
		{
			op:            OpTypeWrite,
			writeSize:     5 * writeSizePrecision, // five KB
			writeDuration: 100 * time.Millisecond,
			fn:            func(f File) { f.Write(fiveKB) },
			expectStall:   true,
		},
		{
			op:            OpTypeSync,
			writeSize:     0,
			writeDuration: 100 * time.Millisecond,
			fn:            func(f File) { f.Sync() },
			expectStall:   true,
		},
		// No false positives.
		{
			op:               OpTypeWrite,
			writeSize:        5,
			writeDuration:    25 * time.Millisecond,
			fn:               func(f File) { f.Write([]byte("uh oh")) },
			createWriteDelta: 100 * time.Millisecond,
			expectStall:      false,
		},
		{
			op:            OpTypeSync,
			writeSize:     0,
			writeDuration: 25 * time.Millisecond,
			fn:            func(f File) { f.Sync() },
			expectStall:   false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.op.String(), func(t *testing.T) {
			diskSlow := make(chan DiskSlowInfo, 1)
			mockFS := &mockFS{create: func(name string) (File, error) {
				return mockFile{syncAndWriteDuration: tc.writeDuration}, nil
			}}
			fs, closer := WithDiskHealthChecks(mockFS, slowThreshold,
				func(info DiskSlowInfo) {
					diskSlow <- info
				})
			defer closer.Close()
			dhFile, _ := fs.Create("test")
			defer dhFile.Close()

			// Writing after file creation tests computation of delta between file
			// creation time & write time.
			time.Sleep(tc.createWriteDelta)

			tc.fn(dhFile)

			if tc.expectStall { // no false negatives
				select {
				case i := <-diskSlow:
					d := i.Duration
					if d.Seconds() < slowThreshold.Seconds() {
						t.Fatalf("expected %0.1f to be greater than threshold %0.1f", d.Seconds(), slowThreshold.Seconds())
					}
					require.Equal(t, tc.writeSize, i.WriteSize)
					require.Equal(t, tc.op, i.OpType)
				case <-time.After(200 * time.Millisecond):
					t.Fatal("disk stall detector did not detect slow disk operation")
				}
			} else { // no false positives
				select {
				case <-diskSlow:
					t.Fatal("disk stall detector detected a slow disk operation")
				case <-time.After(200 * time.Millisecond):
					return
				}
			}
		})
	}
}

func TestDiskHealthChecking_NotTooManyOps(t *testing.T) {
	numBitsForOpType := 64 - deltaBits - writeSizeBits
	numOpTypesAllowed := int(math.Pow(2, float64(numBitsForOpType)))
	numOpTypes := int(opTypeMax)
	require.LessOrEqual(t, numOpTypes, numOpTypesAllowed)
}

func TestDiskHealthChecking_File_PackingAndUnpacking(t *testing.T) {
	testCases := []struct {
		desc          string
		delta         time.Duration
		writeSize     int64
		opType        OpType
		wantDelta     time.Duration
		wantWriteSize int
	}{
		// Write op with write size in bytes.
		{
			desc:          "write, sized op",
			delta:         3000 * time.Millisecond,
			writeSize:     1024, // 1 KB.
			opType:        OpTypeWrite,
			wantDelta:     3000 * time.Millisecond,
			wantWriteSize: 1024,
		},
		// Sync op. No write size. Max-ish delta that packing scheme can handle.
		{
			desc:          "sync, no write size",
			delta:         34 * time.Hour * 24 * 365,
			writeSize:     0,
			opType:        OpTypeSync,
			wantDelta:     34 * time.Hour * 24 * 365,
			wantWriteSize: 0,
		},
		// Delta is negative (e.g. due to clock sync). Set to
		// zero.
		{
			desc:          "delta negative",
			delta:         -5,
			writeSize:     5120, // 5 KB
			opType:        OpTypeWrite,
			wantDelta:     0,
			wantWriteSize: 5120,
		},
		// Write size in bytes is larger than can fit in 20 bits.
		// Round down to max that can fit in 20 bits.
		{
			desc:          "write size truncated",
			delta:         231 * time.Millisecond,
			writeSize:     2097152000, // too big!
			opType:        OpTypeWrite,
			wantDelta:     231 * time.Millisecond,
			wantWriteSize: 1073740800, // (2^20-1) * writeSizePrecision ~= a bit less than one GB
		},
		// Write size in bytes is max representable less than the ceiling.
		{
			desc:          "write size barely not truncated",
			delta:         231 * time.Millisecond,
			writeSize:     1073739776, // max representable less than the ceiling
			opType:        OpTypeWrite,
			wantDelta:     231 * time.Millisecond,
			wantWriteSize: 1073739776, // since can fit, unchanged
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			packed := pack(tc.delta, tc.writeSize, tc.opType)
			gotDelta, gotWriteSize, gotOpType := unpack(packed)

			require.Equal(t, tc.wantDelta, gotDelta)
			require.Equal(t, tc.wantWriteSize, gotWriteSize)
			require.Equal(t, tc.opType, gotOpType)
		})
	}
}

func TestDiskHealthChecking_File_Underflow(t *testing.T) {
	f := &mockFile{}
	hcFile := newDiskHealthCheckingFile(f, 1*time.Second, func(opType OpType, writeSizeInBytes int, duration time.Duration) {
		// We expect to panic before sending the event.
		t.Fatalf("unexpected slow disk event")
	})
	defer hcFile.Close()

	t.Run("too large delta leads to panic", func(t *testing.T) {
		// Given the packing scheme, 35 years of process uptime will lead to a delta
		// that is too large to fit in the packed int64.
		tCreate := time.Now().Add(-35 * time.Hour * 24 * 365)
		hcFile.createTime = tCreate

		// Assert that the time since tCreate (in milliseconds) is indeed greater
		// than the max delta that can fit.
		require.True(t, time.Since(tCreate).Milliseconds() > 1<<deltaBits-1)

		// Attempting to start the clock for a new operation on the file should
		// trigger a panic, as the calculated delta from the file creation time would
		// result in integer overflow.
		require.Panics(t, func() { _, _ = hcFile.Write([]byte("uh oh")) })
	})
	t.Run("pretty large delta but not too large leads to no panic", func(t *testing.T) {
		// Given the packing scheme, 34 years of process uptime will lead to a delta
		// that is just small enough to fit in the packed int64.
		tCreate := time.Now().Add(-34 * time.Hour * 24 * 365)
		hcFile.createTime = tCreate

		require.True(t, time.Since(tCreate).Milliseconds() < 1<<deltaBits-1)
		require.NotPanics(t, func() { _, _ = hcFile.Write([]byte("should be fine")) })
	})
}

var (
	errInjected = errors.New("injected error")
)

func filesystemOpsMockFS(sleepDur time.Duration) *mockFS {
	return &mockFS{
		create: func(name string) (File, error) {
			time.Sleep(sleepDur)
			return nil, errInjected
		},
		link: func(oldname, newname string) error {
			time.Sleep(sleepDur)
			return errInjected
		},
		mkdirAll: func(string, os.FileMode) error {
			time.Sleep(sleepDur)
			return errInjected
		},
		remove: func(name string) error {
			time.Sleep(sleepDur)
			return errInjected
		},
		removeAll: func(name string) error {
			time.Sleep(sleepDur)
			return errInjected
		},
		rename: func(oldname, newname string) error {
			time.Sleep(sleepDur)
			return errInjected
		},
		reuseForWrite: func(oldname, newname string) (File, error) {
			time.Sleep(sleepDur)
			return nil, errInjected
		},
	}
}

func stallFilesystemOperations(fs FS) []filesystemOperation {
	return []filesystemOperation{
		{
			"create", OpTypeCreate, func() { _, _ = fs.Create("foo") },
		},
		{
			"link", OpTypeLink, func() { _ = fs.Link("foo", "bar") },
		},
		{
			"mkdirall", OpTypeMkdirAll, func() { _ = fs.MkdirAll("foo", os.ModePerm) },
		},
		{
			"remove", OpTypeRemove, func() { _ = fs.Remove("foo") },
		},
		{
			"removeall", OpTypeRemoveAll, func() { _ = fs.RemoveAll("foo") },
		},
		{
			"rename", OpTypeRename, func() { _ = fs.Rename("foo", "bar") },
		},
		{
			"reuseforwrite", OpTypeReuseForWrite, func() { _, _ = fs.ReuseForWrite("foo", "bar") },
		},
	}
}

type filesystemOperation struct {
	name   string
	opType OpType
	f      func()
}

func TestDiskHealthChecking_Filesystem(t *testing.T) {
	const sleepDur = 50 * time.Millisecond
	const stallThreshold = 10 * time.Millisecond

	// Wrap with disk-health checking, counting each stall via stallCount.
	var expectedOpType OpType
	var stallCount atomic.Uint64
	fs, closer := WithDiskHealthChecks(filesystemOpsMockFS(sleepDur), stallThreshold,
		func(info DiskSlowInfo) {
			require.Equal(t, 0, info.WriteSize)
			require.Equal(t, expectedOpType, info.OpType)
			stallCount.Add(1)
		})
	defer closer.Close()
	fs.(*diskHealthCheckingFS).tickInterval = 5 * time.Millisecond
	ops := stallFilesystemOperations(fs)
	for _, o := range ops {
		t.Run(o.name, func(t *testing.T) {
			expectedOpType = o.opType
			before := stallCount.Load()
			o.f()
			after := stallCount.Load()
			require.Greater(t, int(after-before), 0)
		})
	}
}

// TestDiskHealthChecking_Filesystem_Close tests the behavior of repeatedly
// closing and reusing a filesystem wrapped by WithDiskHealthChecks. This is a
// permitted usage because it allows (*pebble.Options).EnsureDefaults to wrap
// with disk-health checking by default, and to clean up the long-running
// goroutine on (*pebble.DB).Close, while still allowing the FS to be used
// multiple times.
func TestDiskHealthChecking_Filesystem_Close(t *testing.T) {
	const stallThreshold = 10 * time.Millisecond
	mockFS := &mockFS{
		create: func(name string) (File, error) {
			time.Sleep(50 * time.Millisecond)
			return &mockFile{}, nil
		},
	}

	stalled := map[string]time.Duration{}
	fs, closer := WithDiskHealthChecks(mockFS, stallThreshold,
		func(info DiskSlowInfo) { stalled[info.Path] = info.Duration })
	fs.(*diskHealthCheckingFS).tickInterval = 5 * time.Millisecond

	files := []string{"foo", "bar", "bax"}
	for _, filename := range files {
		// Create will stall, and the detector should write to the stalled map
		// with the filename.
		_, _ = fs.Create(filename)
		// Invoke the closer. This will cause the long-running goroutine to
		// exit, but the fs should still be usable and should still detect
		// subsequent stalls on the next iteration.
		require.NoError(t, closer.Close())
		require.Contains(t, stalled, filename)
	}
}
