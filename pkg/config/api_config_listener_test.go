/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"errors"
	"sync"
	"testing"
	"time"
)

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type recordingAPIConfigListener struct {
	resourceAdds      []Resource
	resourceDeletes   []Resource
	methodAdds        []Method
	resourceAddSignal chan Resource
}

func (l *recordingAPIConfigListener) ResourceChange(Resource, Resource) bool { return true }

func (l *recordingAPIConfigListener) ResourceAdd(resource Resource) bool {
	l.resourceAdds = append(l.resourceAdds, resource)
	if l.resourceAddSignal != nil {
		l.resourceAddSignal <- resource
	}
	return true
}

func (l *recordingAPIConfigListener) ResourceDelete(resource Resource) bool {
	l.resourceDeletes = append(l.resourceDeletes, resource)
	return true
}

func (l *recordingAPIConfigListener) MethodChange(Resource, Method, Method) bool { return true }

func (l *recordingAPIConfigListener) MethodAdd(_ Resource, method Method) bool {
	l.methodAdds = append(l.methodAdds, method)
	return true
}

func (l *recordingAPIConfigListener) MethodDelete(Resource, Method) bool { return true }

func TestHandlePutEventAddsFirstResourceAfterEmptySnapshot(t *testing.T) {
	previousConfig := apiConfig
	previousListener := listener
	t.Cleanup(func() {
		apiConfig = previousConfig
		listener = previousListener
	})

	apiConfig = &APIConfig{Resources: make([]Resource, 0)}
	recorder := &recordingAPIConfigListener{}
	listener = recorder

	resourceKey := []byte("/pixiu/config/api/resources/7")
	resourceValue := []byte(`
id: 7
type: restful
path: /api/v1/users/:id
timeout: 2s
`)
	handlePutEvent(resourceKey, resourceValue)

	require.Len(t, apiConfig.Resources, 1)
	assert.Equal(t, 7, apiConfig.Resources[0].ID)
	assert.Equal(t, "/api/v1/users/:id", apiConfig.Resources[0].Path)
	require.Len(t, recorder.resourceAdds, 1)

	methodKey := []byte("/pixiu/config/api/resources/7/method/9")
	methodValue := []byte(`
id: 9
resourcePath: /api/v1/users/:id
httpVerb: GET
enable: true
timeout: 2s
`)
	handlePutEvent(methodKey, methodValue)

	require.Len(t, apiConfig.Resources[0].Methods, 1)
	assert.Equal(t, 9, apiConfig.Resources[0].Methods[0].ID)
	assert.Equal(t, "GET", apiConfig.Resources[0].Methods[0].HTTPVerb)
	require.Len(t, recorder.methodAdds, 1)
}

type fakeAPIConfigSource struct {
	revision      int64
	events        chan clientv3.WatchResponse
	done          chan struct{}
	watchRevision chan int64
}

func (s *fakeAPIConfigSource) Snapshot(string) ([]string, []string, int64, error) {
	return nil, nil, s.revision, nil
}

func (s *fakeAPIConfigSource) Watch(_ string, revision int64) (clientv3.WatchChan, error) {
	s.watchRevision <- revision
	return s.events, nil
}

func (s *fakeAPIConfigSource) Done() <-chan struct{} {
	return s.done
}

type apiConfigSnapshot struct {
	keys     []string
	values   []string
	revision int64
}

type recoveringAPIConfigSource struct {
	mu             sync.Mutex
	snapshots      []apiConfigSnapshot
	watchChannels  []chan clientv3.WatchResponse
	snapshotCalls  int
	watchCalls     int
	watchRevisions chan int64
	done           chan struct{}
}

func (s *recoveringAPIConfigSource) Snapshot(string) ([]string, []string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshotCalls >= len(s.snapshots) {
		return nil, nil, 0, errors.New("unexpected api config snapshot")
	}
	snapshot := s.snapshots[s.snapshotCalls]
	s.snapshotCalls++
	return append([]string(nil), snapshot.keys...), append([]string(nil), snapshot.values...), snapshot.revision, nil
}

func (s *recoveringAPIConfigSource) Watch(_ string, revision int64) (clientv3.WatchChan, error) {
	s.mu.Lock()
	if s.watchCalls >= len(s.watchChannels) {
		s.mu.Unlock()
		return nil, errors.New("unexpected api config watch")
	}
	watch := s.watchChannels[s.watchCalls]
	s.watchCalls++
	s.mu.Unlock()
	s.watchRevisions <- revision
	return watch, nil
}

func (s *recoveringAPIConfigSource) Done() <-chan struct{} {
	return s.done
}

func TestEmptySnapshotWatchStartsAtNextRevisionAndDeliversFirstResource(t *testing.T) {
	previousConfig := apiConfig
	previousListener := listener
	t.Cleanup(func() {
		apiConfig = previousConfig
		listener = previousListener
	})

	source := &fakeAPIConfigSource{
		revision:      41,
		events:        make(chan clientv3.WatchResponse, 1),
		done:          make(chan struct{}),
		watchRevision: make(chan int64, 1),
	}
	loaded, watchRevision, err := loadAPIConfigFromSource(source, "/pixiu/config/api")
	require.NoError(t, err)
	require.Empty(t, loaded.Resources)
	assert.Equal(t, int64(42), watchRevision)

	recorder := &recordingAPIConfigListener{resourceAddSignal: make(chan Resource, 1)}
	listener = recorder
	source.events <- clientv3.WatchResponse{Events: []*clientv3.Event{{
		Type: mvccpb.PUT,
		Kv: &mvccpb.KeyValue{
			Key:   []byte("/pixiu/config/api/resources/7"),
			Value: []byte("id: 7\ntype: restful\npath: /api/v1/users/:id\ntimeout: 2s\n"),
		},
	}}}
	ready := make(chan struct{})
	watchResult := make(chan bool, 1)
	go func() {
		watchResult <- listenResourceAndMethodEvent(source, "/pixiu/config/api", watchRevision, ready)
	}()

	assert.Equal(t, int64(42), <-source.watchRevision)
	select {
	case <-recorder.resourceAddSignal:
		t.Fatal("watch event was delivered before the initial snapshot became ready")
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	select {
	case resource := <-recorder.resourceAddSignal:
		assert.Equal(t, 7, resource.ID)
		assert.Equal(t, "/api/v1/users/:id", resource.Path)
	case <-time.After(2 * time.Second):
		t.Fatal("first resource event was not delivered")
	}
	close(source.done)
	assert.False(t, <-watchResult)
	require.Len(t, apiConfig.Resources, 1)
}

func TestWatchRecoveryReloadsSnapshotAndContinuesDelivery(t *testing.T) {
	previousConfig := apiConfig
	previousListener := listener
	t.Cleanup(func() {
		apiConfig = previousConfig
		listener = previousListener
	})

	firstWatch := make(chan clientv3.WatchResponse, 1)
	secondWatch := make(chan clientv3.WatchResponse, 1)
	source := &recoveringAPIConfigSource{
		snapshots: []apiConfigSnapshot{
			{
				keys: []string{
					"/pixiu/config/api/resources/1",
					"/pixiu/config/api/resources/1/method/11",
				},
				values: []string{
					"id: 1\ntype: restful\npath: /initial\ntimeout: 2s\n",
					"id: 11\nresourcePath: /initial\nhttpVerb: GET\nenable: true\ntimeout: 2s\n",
				},
				revision: 10,
			},
			{
				keys: []string{
					"/pixiu/config/api/resources/2",
					"/pixiu/config/api/resources/2/method/22",
				},
				values: []string{
					"id: 2\ntype: restful\npath: /recovered\ntimeout: 2s\n",
					"id: 22\nresourcePath: /recovered\nhttpVerb: GET\nenable: true\ntimeout: 2s\n",
				},
				revision: 20,
			},
		},
		watchChannels:  []chan clientv3.WatchResponse{firstWatch, secondWatch},
		watchRevisions: make(chan int64, 2),
		done:           make(chan struct{}),
	}
	loaded, watchRevision, err := loadAPIConfigFromSource(source, "/pixiu/config/api")
	require.NoError(t, err)
	require.Len(t, loaded.Resources, 1)
	assert.Equal(t, int64(11), watchRevision)

	recorder := &recordingAPIConfigListener{resourceAddSignal: make(chan Resource, 2)}
	listener = recorder
	finished := make(chan struct{})
	started := false
	t.Cleanup(func() {
		if !started {
			return
		}
		select {
		case <-source.done:
		default:
			close(source.done)
		}
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("watch recovery did not stop after source shutdown")
		}
	})
	started = true
	go func() {
		defer close(finished)
		watchAPIConfigWithRecovery(source, "/pixiu/config/api", watchRevision, nil)
	}()

	assert.Equal(t, int64(11), <-source.watchRevisions)
	firstWatch <- clientv3.WatchResponse{Canceled: true, CompactRevision: 10}

	select {
	case revision := <-source.watchRevisions:
		assert.Equal(t, int64(21), revision)
	case <-time.After(2 * time.Second):
		t.Fatal("watch recovery did not start from the fresh snapshot revision")
	}
	select {
	case resource := <-recorder.resourceAddSignal:
		assert.Equal(t, 2, resource.ID)
		assert.Len(t, resource.Methods, 1)
	case <-time.After(2 * time.Second):
		t.Fatal("fresh snapshot was not reconciled into the runtime listener")
	}
	require.Len(t, recorder.resourceDeletes, 1)
	assert.Equal(t, 1, recorder.resourceDeletes[0].ID)

	secondWatch <- clientv3.WatchResponse{Events: []*clientv3.Event{{
		Type: mvccpb.PUT,
		Kv: &mvccpb.KeyValue{
			Key:   []byte("/pixiu/config/api/resources/3"),
			Value: []byte("id: 3\ntype: restful\npath: /later\ntimeout: 2s\n"),
		},
	}}}
	select {
	case resource := <-recorder.resourceAddSignal:
		assert.Equal(t, 3, resource.ID)
		assert.Equal(t, "/later", resource.Path)
	case <-time.After(2 * time.Second):
		t.Fatal("event after watch recovery did not reach the runtime listener")
	}

	close(source.done)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("watch recovery did not stop after source shutdown")
	}
}

var _ APIConfigResourceListener = (*recordingAPIConfigListener)(nil)
var _ apiConfigSource = (*fakeAPIConfigSource)(nil)
var _ apiConfigSource = (*recoveringAPIConfigSource)(nil)
