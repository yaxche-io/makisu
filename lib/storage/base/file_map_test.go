//  Copyright (c) 2018 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package base

import (
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests should pass for all FileMap implementations
func TestFileMap(t *testing.T) {
	fileMaps := []struct {
		name    string
		fixture func() (bundle *fileMapTestBundle, cleanup func())
	}{
		{"SimpleFileMap", fileMapSimpleFixture},
		{"LRUFileMap", fileMapLRUFixture},
	}

	tests := []func(require *require.Assertions, bundle *fileMapTestBundle){
		testFileMapLoadOrStore,
		testFileMapLoadOrStoreAborts,
		testFileMapLoadForRead,
		testFileMapLoadForWrite,
		testFileMapDelete,
		testFileMapDeleteAbort,
	}

	for _, fm := range fileMaps {
		t.Run(fm.name, func(t *testing.T) {
			for _, test := range tests {
				testName := runtime.FuncForPC(reflect.ValueOf(test).Pointer()).Name()
				t.Run(testName, func(t *testing.T) {
					require := require.New(t)
					s, cleanup := fm.fixture()
					defer cleanup()
					test(require, s)
				})
			}
		})
	}
}

func testFileMapLoadOrStore(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	fm := bundle.fm

	require.False(fm.Contains(fe.GetName()))

	var wg sync.WaitGroup
	var successCount, skippedCount, errCount uint32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			_, loaded := fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
				err = fe.Create(s1, 0)
				return err
			})
			if loaded {
				atomic.AddUint32(&skippedCount, 1)
			} else if err != nil {
				atomic.AddUint32(&errCount, 1)
			} else {
				atomic.AddUint32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// Only one goroutine successfully stored the entry.
	require.Equal(errCount, uint32(0))
	require.Equal(skippedCount, uint32(99))
	require.Equal(successCount, uint32(1))

	require.True(fm.Contains(fe.GetName()))
}

func testFileMapLoadOrStoreAborts(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	fm := bundle.fm

	err := fe.Create(s1, 0)
	require.NoError(err)

	var wg sync.WaitGroup
	var successCount, skippedCount, errorCount uint32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			_, loaded := fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
				// Exit right away.
				err = os.ErrNotExist
				return err
			})
			if loaded {
				atomic.AddUint32(&skippedCount, 1)
			} else if err != nil {
				atomic.AddUint32(&errorCount, 1)
			} else {
				atomic.AddUint32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// Some goroutines successfully stored the entry, executed f, encountered failure and removed the entry.
	// Others might have loaded the temp entries and skipped.
	require.True(errorCount >= uint32(1))
	require.True(errorCount+skippedCount == uint32(100))
	require.Equal(successCount, uint32(0))
}

func testFileMapLoadForRead(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	fm := bundle.fm

	err := fe.Create(s1, 0)
	require.NoError(err)

	// Loading an non-existent entry does nothing.
	testInt := 1
	loaded := fm.LoadForWrite(fe.GetName(), func(name string, entry FileEntry) {
		testInt = 2
		return
	})
	require.False(loaded)
	require.Equal(testInt, 1)

	// Put entry into map.
	_, loaded = fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
		return nil
	})
	require.False(loaded)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loaded := fm.LoadForRead(fe.GetName(), func(name string, entry FileEntry) {
				_, err := fe.GetStat()
				require.NoError(err)
			})
			require.True(loaded)
		}()
	}
	wg.Wait()
}

func testFileMapLoadForWrite(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	s2 := bundle.state2
	fm := bundle.fm

	err := fe.Create(s1, 0)
	require.NoError(err)

	// Loading an non-existent entry does nothing.
	testInt := 1
	loaded := fm.LoadForWrite(fe.GetName(), func(name string, entry FileEntry) {
		testInt = 2
		return
	})
	require.False(loaded)
	require.Equal(testInt, 1)

	// Put entry into map.
	_, loaded = fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
		return nil
	})
	require.False(loaded)

	var wg sync.WaitGroup
	var successCount, stateErrorCount, otherErrorCount uint32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			loaded := fm.LoadForWrite(fe.GetName(), func(name string, entry FileEntry) {
				if fe.GetState() == s2 {
					atomic.AddUint32(&stateErrorCount, 1)
				} else {
					err = fe.Move(s2)
					if err == nil {
						atomic.AddUint32(&successCount, 1)
					} else {
						atomic.AddUint32(&otherErrorCount, 1)
					}
				}
			})
			require.True(loaded)
		}()
	}
	wg.Wait()

	// Only first goroutine successfully executed Move(), the others encountered FileStateError.
	require.Equal(otherErrorCount, uint32(0))
	require.Equal(stateErrorCount, uint32(99))
	require.Equal(successCount, uint32(1))
}

func testFileMapDelete(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	fm := bundle.fm

	// Put entry into map.
	var err error
	_, loaded := fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
		err = fe.Create(s1, 0)
		return err
	})
	require.False(loaded)
	require.NoError(err)

	var wg sync.WaitGroup
	var successCount, skippedCount, errorCount uint32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error

			deleted := fm.Delete(fe.GetName(), func(name string, entry FileEntry) error {
				err = fe.Delete()
				return err
			})
			if err != nil {
				atomic.AddUint32(&errorCount, 1)
			} else if deleted {
				atomic.AddUint32(&successCount, 1)
			} else {
				atomic.AddUint32(&skippedCount, 1)
			}
		}()
	}
	wg.Wait()

	// Only the first goroutine successfully deleted the entry, the others skipped.
	require.Equal(errorCount, uint32(0))
	require.Equal(skippedCount, uint32(99))
	require.Equal(successCount, uint32(1))
}

func testFileMapDeleteAbort(require *require.Assertions, bundle *fileMapTestBundle) {
	fe := bundle.entry
	s1 := bundle.state1
	fm := bundle.fm

	// Put entry into map.
	var err error
	_, loaded := fm.LoadOrStore(fe.GetName(), fe, func(name string, entry FileEntry) error {
		err = fe.Create(s1, 0)
		return err
	})
	require.False(loaded)
	require.NoError(err)

	var wg sync.WaitGroup
	var successCount, skippedCount, errorCount uint32
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error

			deleted := fm.Delete(fe.GetName(), func(name string, entry FileEntry) error {
				err = os.ErrNotExist
				return nil
			})
			if err != nil {
				atomic.AddUint32(&errorCount, 1)
			} else if deleted {
				atomic.AddUint32(&successCount, 1)
			} else {
				atomic.AddUint32(&skippedCount, 1)
			}
		}()
	}
	wg.Wait()

	// The first goroutine encountered error but removed the entry from map anyway.
	// Other goroutines skipped.
	require.Equal(errorCount, uint32(1))
	require.Equal(skippedCount, uint32(99))
	require.Equal(successCount, uint32(0))
}
