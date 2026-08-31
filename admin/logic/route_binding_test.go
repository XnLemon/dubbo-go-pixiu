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

package logic

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

import (
	adminconfig "github.com/apache/dubbo-go-pixiu/admin/config"
	"github.com/apache/dubbo-go-pixiu/pkg/common/yaml"
	"github.com/apache/dubbo-go-pixiu/pkg/config"
	adminschema "github.com/apache/dubbo-go-pixiu/pkg/config/schema"
	apiconfig "github.com/apache/dubbo-go-pixiu/pkg/filter/http/apiconfig/api"
	"github.com/apache/dubbo-go-pixiu/pkg/filter/http/remote"
)

type memoryRouteBindingStore struct {
	mu       sync.Mutex
	values   map[string]string
	counters map[string]int
	commits  []memoryRouteBindingCommit
}

type memoryRouteBindingCommit struct {
	puts    []RouteBindingKV
	deletes []string
}

func newMemoryRouteBindingStore() *memoryRouteBindingStore {
	return &memoryRouteBindingStore{
		values:   make(map[string]string),
		counters: make(map[string]int),
	}
}

func (s *memoryRouteBindingStore) Get(key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.values[key]
	return value, exists, nil
}

func (s *memoryRouteBindingStore) List(prefix string) ([]RouteBindingKV, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]RouteBindingKV, 0)
	for key, value := range s.values {
		if strings.HasPrefix(key, prefix) {
			items = append(items, RouteBindingKV{Key: key, Value: value})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *memoryRouteBindingStore) Put(key, value string) error {
	s.mu.Lock()
	s.values[key] = value
	s.mu.Unlock()
	return nil
}

func (s *memoryRouteBindingStore) Delete(key string) error {
	s.mu.Lock()
	delete(s.values, key)
	s.mu.Unlock()
	return nil
}

func (s *memoryRouteBindingStore) NextID(counterKey string) (int, error) {
	s.mu.Lock()
	s.counters[counterKey]++
	id := s.counters[counterKey]
	s.mu.Unlock()
	return id, nil
}

func (s *memoryRouteBindingStore) Commit(puts []RouteBindingKV, deletes []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, put := range puts {
		if strings.TrimSpace(put.Key) == "" {
			return errors.New("route binding test commit key is empty")
		}
	}
	for _, key := range deletes {
		if strings.TrimSpace(key) == "" {
			return errors.New("route binding test delete key is empty")
		}
	}
	for _, key := range deletes {
		delete(s.values, key)
	}
	for _, put := range puts {
		s.values[put.Key] = put.Value
	}
	s.commits = append(s.commits, memoryRouteBindingCommit{
		puts:    append([]RouteBindingKV(nil), puts...),
		deletes: append([]string(nil), deletes...),
	})
	return nil
}

func (s *memoryRouteBindingStore) value(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.values[key]
	return value, exists
}

func installRouteBindingBootstrap(t *testing.T) {
	t.Helper()
	previous := adminconfig.Bootstrap
	adminconfig.Bootstrap = &adminconfig.AdminBootstrap{
		EtcdConfig: adminconfig.EtcdConfig{Path: "/pixiu/config/api"},
	}
	t.Cleanup(func() { adminconfig.Bootstrap = previous })
}

func newRouteBindingServiceForTest(t *testing.T, store RouteBindingStore) *RouteBindingService {
	t.Helper()
	registry, err := adminschema.NewBuiltinRegistry()
	require.NoError(t, err)
	service, err := NewRouteBindingService(store, registry)
	require.NoError(t, err)
	return service
}

func TestRouteBindingServiceLifecycleProjectsLegacyKeysOnlyOnPublish(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	object := routeBindingTestObject("user-get", "GetUser")
	draft, err := service.SaveDraft(object)
	require.NoError(t, err)
	assert.Equal(t, 1, draft.ResourceID)
	assert.Equal(t, 1, draft.MethodID)
	assert.Equal(t, uint64(1), draft.Revision)
	assert.Equal(t, "draft", draft.Object.Spec["publish"].(map[string]any)["mode"])

	_, exists, err := store.Get(space.draftKey("user-get"))
	require.NoError(t, err)
	assert.True(t, exists)
	_, exists, err = store.Get(space.resourceKey(draft.ResourceID))
	require.NoError(t, err)
	assert.False(t, exists, "saving a draft must not write the runtime resource key")
	_, exists, err = store.Get(space.methodKey(draft.ResourceID, draft.MethodID))
	require.NoError(t, err)
	assert.False(t, exists, "saving a draft must not write the runtime method key")

	list, err := service.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "draft", list[0].Status)

	preview, err := service.PreviewSaved("user-get")
	require.NoError(t, err)
	assert.Contains(t, preview.LegacyYAML, "path: /api/v1/users/:id")
	assert.Empty(t, preview.Diff)

	// The generated legacy fragments must be loadable by the same structures
	// consumed by Pixiu's runtime watcher.
	var resource config.Resource
	var method config.Method
	require.NoError(t, yaml.UnmarshalYML([]byte(draft.ResourceYAML), &resource))
	require.NoError(t, yaml.UnmarshalYML([]byte(draft.MethodYAML), &method))
	assert.Equal(t, 2*time.Second, resource.Timeout)
	assert.Equal(t, 2*time.Second, method.Timeout)
	resource.Methods = []config.Method{method}

	discovery := apiconfig.NewLocalMemoryAPIDiscoveryService()
	require.NoError(t, discovery.InitAPIsFromConfig(config.APIConfig{Resources: []config.Resource{resource}}))
	matched, err := discovery.MatchAPI("/api/v1/users/42", http.MethodGet)
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/users/:id", matched.URLPattern)
	outbound, err := (&remote.DubboHandler{}).BuildOutbound(
		httpRequestForRouteTest(t, "http://gateway.local/api/v1/users/42"),
		matched,
	)
	require.NoError(t, err)
	assert.Equal(t, "com.example.UserService", outbound.Service)
	assert.Equal(t, "GetUser", outbound.Method)
	assert.Equal(t, []any{"42"}, outbound.Arguments)
	assert.Equal(t, []string{"java.lang.String"}, outbound.ParamTypes)

	published, err := service.Publish("user-get")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), published.Revision)
	assert.Equal(t, "published", published.Object.Spec["publish"].(map[string]any)["mode"])
	require.Len(t, store.commits, 1)
	assertPublishedProjectionOrderAndRoute(t, store.commits[0], space, published)
	_, exists, err = store.Get(space.resourceKey(published.ResourceID))
	require.NoError(t, err)
	assert.True(t, exists)
	_, exists, err = store.Get(space.methodKey(published.ResourceID, published.MethodID))
	require.NoError(t, err)
	assert.True(t, exists)

	changed := object.Clone()
	changed.Spec["target"].(map[string]any)["method"] = "GetUserV2"
	changedDraft, err := service.SaveDraft(changed)
	require.NoError(t, err)
	assert.Equal(t, draft.ResourceID, changedDraft.ResourceID)
	assert.Equal(t, draft.MethodID, changedDraft.MethodID)
	assert.Equal(t, uint64(2), changedDraft.Revision)
	oldMethodValue, oldMethodExists := store.value(space.methodKey(draft.ResourceID, draft.MethodID))
	require.True(t, oldMethodExists)

	changedPreview, err := service.PreviewSaved("user-get")
	require.NoError(t, err)
	assert.Contains(t, changedPreview.Diff, "-            method: GetUser\n")
	assert.Contains(t, changedPreview.Diff, "+            method: GetUserV2\n")
	currentMethodValue, currentMethodExists := store.value(space.methodKey(draft.ResourceID, draft.MethodID))
	assert.True(t, currentMethodExists)
	assert.Equal(t, oldMethodValue, currentMethodValue, "a changed draft must not alter the runtime method key")

	// Publishing the modified draft creates a second immutable history entry.
	secondDraft, err := service.SaveDraft(changed)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), secondDraft.Revision)
	secondPublished, err := service.Publish("user-get")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), secondPublished.Revision)

	history, err := service.History("user-get")
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, uint64(2), history[0].Revision)
	assert.Equal(t, uint64(1), history[1].Revision)
	assert.Contains(t, history[0].Object.Spec["target"].(map[string]any)["method"], "V2")
	assert.Equal(t, "GetUser", history[1].Object.Spec["target"].(map[string]any)["method"])

	list, err = service.List()
	require.NoError(t, err)
	assert.Equal(t, "published", list[0].Status, "draft and published legacy projections are equal after publish")

	rolledBack, err := service.Rollback("user-get", 0)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), rolledBack.Revision)
	assert.Equal(t, "GetUser", rolledBack.Object.Spec["target"].(map[string]any)["method"])
	rolledBackMethod, exists := store.value(space.methodKey(rolledBack.ResourceID, rolledBack.MethodID))
	require.True(t, exists)
	assert.Contains(t, rolledBackMethod, "method: GetUser")

	history, err = service.History("user-get")
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, uint64(3), history[0].Revision)

	// Discarding a draft must not remove the already published projection.
	require.NoError(t, service.DeleteDraft("user-get"))
	detail, err := service.Get("user-get")
	require.NoError(t, err)
	assert.Nil(t, detail.Draft)
	assert.NotNil(t, detail.Published)
	assert.Equal(t, uint64(3), detail.Published.Revision)
	_, exists = store.value(space.publishedKey("user-get"))
	assert.True(t, exists)
}

func TestRouteBindingServiceRollbackReplaysStoredSnapshot(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	_, err = service.SaveDraft(routeBindingTestObject("snapshot-route", "GetUser"))
	require.NoError(t, err)
	first, err := service.Publish("snapshot-route")
	require.NoError(t, err)

	historyValue, exists := store.value(space.historyKey("snapshot-route", first.Revision))
	require.True(t, exists)
	snapshot, err := decodeRouteBindingRecord(historyValue)
	require.NoError(t, err)
	snapshot.ResourceYAML += "# generated by the original compiler\n"
	snapshot.MethodYAML += "# generated by the original compiler\n"
	snapshot.LegacyYAML += "# generated by the original compiler\n"
	encodedSnapshot, err := encodeRouteBindingRecord(snapshot)
	require.NoError(t, err)
	require.NoError(t, store.Put(space.historyKey("snapshot-route", first.Revision), encodedSnapshot))

	changed := routeBindingTestObject("snapshot-route", "GetUserV2")
	_, err = service.SaveDraft(changed)
	require.NoError(t, err)
	_, err = service.Publish("snapshot-route")
	require.NoError(t, err)

	rolledBack, err := service.Rollback("snapshot-route", first.Revision)
	require.NoError(t, err)
	assert.Equal(t, snapshot.ResourceYAML, rolledBack.ResourceYAML)
	assert.Equal(t, snapshot.MethodYAML, rolledBack.MethodYAML)
	assert.Equal(t, snapshot.LegacyYAML, rolledBack.LegacyYAML)

	resourceValue, exists := store.value(space.resourceKey(first.ResourceID))
	require.True(t, exists)
	assert.Equal(t, snapshot.ResourceYAML, resourceValue)
	methodValue, exists := store.value(space.methodKey(first.ResourceID, first.MethodID))
	require.True(t, exists)
	assert.Equal(t, snapshot.MethodYAML, methodValue)

	detail, err := service.Get("snapshot-route")
	require.NoError(t, err)
	require.NotNil(t, detail.Draft)
	assert.Equal(t, snapshot.ResourceYAML, detail.Draft.ResourceYAML)
	assert.Equal(t, snapshot.MethodYAML, detail.Draft.MethodYAML)
	assert.Equal(t, snapshot.LegacyYAML, detail.Draft.LegacyYAML)
	assert.Equal(t, "draft", detail.Draft.Object.Spec["publish"].(map[string]any)["mode"])
}

func TestRouteBindingServicePublishesCanonicalOnlyChangeWithoutRuntimeChurn(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	registry, err := adminschema.NewBuiltinRegistry()
	require.NoError(t, err)
	require.NoError(t, registry.RegisterField(
		adminschema.KindAdminRouteBinding,
		"extensions.owner",
		adminschema.FieldSchema{Type: adminschema.FieldTypeString},
	))
	service, err := NewRouteBindingService(store, registry)
	require.NoError(t, err)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	object := routeBindingTestObject("canonical-change", "GetUser")
	_, err = service.SaveDraft(object)
	require.NoError(t, err)
	first, err := service.Publish("canonical-change")
	require.NoError(t, err)
	require.Len(t, store.commits, 1)
	resourceBefore, exists := store.value(space.resourceKey(first.ResourceID))
	require.True(t, exists)
	methodBefore, exists := store.value(space.methodKey(first.ResourceID, first.MethodID))
	require.True(t, exists)

	changed := object.Clone()
	changed.Spec["extensions"] = map[string]any{"owner": "team-a"}
	_, err = service.SaveDraft(changed)
	require.NoError(t, err)
	list, err := service.List()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "modified", list[0].Status)

	second, err := service.Publish("canonical-change")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second.Revision)
	assert.Equal(t, "team-a", second.Object.Spec["extensions"].(map[string]any)["owner"])
	require.Len(t, store.commits, 2)
	assert.Equal(t, []string{
		space.publishedKey("canonical-change"),
		space.historyKey("canonical-change", second.Revision),
	}, routeBindingPutKeys(store.commits[1]))
	resourceAfter, exists := store.value(space.resourceKey(second.ResourceID))
	require.True(t, exists)
	methodAfter, exists := store.value(space.methodKey(second.ResourceID, second.MethodID))
	require.True(t, exists)
	assert.Equal(t, resourceBefore, resourceAfter)
	assert.Equal(t, methodBefore, methodAfter)

	idempotent, err := service.Publish("canonical-change")
	require.NoError(t, err)
	assert.Equal(t, second.Revision, idempotent.Revision)
	assert.Len(t, store.commits, 2, "an identical canonical object must be an idempotent publish")
}

func TestRouteBindingServiceRejectsEquivalentLegacyPathOnPublish(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	legacyResource, err := yaml.MarshalYML(config.Resource{
		ID:      99,
		Type:    "restful",
		Path:    "/API/v1/users/:userId/",
		Timeout: time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, store.Put(space.resourceKey(99), string(legacyResource)))

	draft, err := service.SaveDraft(routeBindingTestObject("conflicting-route", "GetUser"))
	require.NoError(t, err)
	_, err = service.Publish("conflicting-route")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicts with legacy resource")
	assert.Empty(t, store.commits)
	_, exists, err := store.Get(space.publishedKey("conflicting-route"))
	require.NoError(t, err)
	assert.False(t, exists)
	_, exists, err = store.Get(space.resourceKey(draft.ResourceID))
	require.NoError(t, err)
	assert.False(t, exists, "a conflicting route must fail before writing its runtime projection")
}

func TestRouteBindingServiceSkipsOccupiedLegacyIDs(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	legacyResource, err := yaml.MarshalYML(config.Resource{
		ID:      1,
		Type:    "restful",
		Path:    "/legacy/users",
		Timeout: time.Second,
	})
	require.NoError(t, err)
	legacyMethod := "id: 1\nresourcePath: /legacy/orphan\nhttpVerb: GET\n"
	require.NoError(t, store.Put(space.resourceKey(1), string(legacyResource)))
	require.NoError(t, store.Put(space.methodKey(2, 1), legacyMethod))

	draft, err := service.SaveDraft(routeBindingTestObject("safe-allocation", "GetUser"))
	require.NoError(t, err)
	assert.Equal(t, 2, draft.ResourceID)
	assert.Equal(t, 2, draft.MethodID)

	published, err := service.Publish("safe-allocation")
	require.NoError(t, err)
	assert.Equal(t, 2, published.ResourceID)
	assert.Equal(t, 2, published.MethodID)
	resourceValue, exists := store.value(space.resourceKey(1))
	require.True(t, exists)
	assert.Equal(t, string(legacyResource), resourceValue)
	methodValue, exists := store.value(space.methodKey(2, 1))
	require.True(t, exists)
	assert.Equal(t, legacyMethod, methodValue)
}

func TestRouteBindingServiceRechecksLegacyIDsBeforeInitialPublish(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	draft, err := service.SaveDraft(routeBindingTestObject("late-collision", "GetUser"))
	require.NoError(t, err)
	assert.Equal(t, 1, draft.ResourceID)
	legacyResource, err := yaml.MarshalYML(config.Resource{
		ID:      draft.ResourceID,
		Type:    "restful",
		Path:    "/legacy/created-after-draft",
		Timeout: time.Second,
	})
	require.NoError(t, err)
	require.NoError(t, store.Put(space.resourceKey(draft.ResourceID), string(legacyResource)))

	published, err := service.Publish("late-collision")
	require.NoError(t, err)
	assert.Equal(t, 2, published.ResourceID)
	legacyValue, exists := store.value(space.resourceKey(draft.ResourceID))
	require.True(t, exists)
	assert.Equal(t, string(legacyResource), legacyValue)
	detail, err := service.Get("late-collision")
	require.NoError(t, err)
	require.NotNil(t, detail.Draft)
	assert.Equal(t, published.ResourceID, detail.Draft.ResourceID)
	assert.Equal(t, published.MethodID, detail.Draft.MethodID)
}

func TestRouteBindingServiceIdempotentPublishReconcilesRuntimeProjection(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	_, err = service.SaveDraft(routeBindingTestObject("reconcile-route", "GetUser"))
	require.NoError(t, err)
	published, err := service.Publish("reconcile-route")
	require.NoError(t, err)
	discovery := apiconfig.NewLocalMemoryAPIDiscoveryService()
	replayRouteBindingRuntimeProjection(t, discovery, store.commits[0], space, published)
	_, err = discovery.MatchAPI("/api/v1/users/42", http.MethodGet)
	require.NoError(t, err)

	var resource config.Resource
	require.NoError(t, yaml.UnmarshalYML([]byte(published.ResourceYAML), &resource))
	assert.True(t, discovery.ResourceDelete(resource))
	_, err = discovery.MatchAPI("/api/v1/users/42", http.MethodGet)
	require.Error(t, err)

	require.NoError(t, store.Delete(space.resourceKey(published.ResourceID)))

	reconciled, err := service.Publish("reconcile-route")
	require.NoError(t, err)
	assert.Equal(t, published.Revision, reconciled.Revision)
	require.Len(t, store.commits, 2)
	assert.Equal(t, []string{
		space.resourceKey(published.ResourceID),
		space.methodKey(published.ResourceID, published.MethodID),
	}, routeBindingPutKeys(store.commits[1]))
	resourceValue, exists := store.value(space.resourceKey(published.ResourceID))
	require.True(t, exists)
	assert.Equal(t, published.ResourceYAML, resourceValue)
	methodValue, exists := store.value(space.methodKey(published.ResourceID, published.MethodID))
	require.True(t, exists)
	assert.Equal(t, published.MethodYAML, methodValue)
	replayRouteBindingRuntimeProjection(t, discovery, store.commits[1], space, reconciled)
	matched, err := discovery.MatchAPI("/api/v1/users/42", http.MethodGet)
	require.NoError(t, err)
	assert.Equal(t, "GetUser", matched.Method.Method)
	history, err := service.History("reconcile-route")
	require.NoError(t, err)
	assert.Len(t, history, 1, "projection reconciliation must not create a new canonical revision")
}

func TestRouteBindingServiceRollbackReconcilesMissingUnchangedProjection(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	registry, err := adminschema.NewBuiltinRegistry()
	require.NoError(t, err)
	require.NoError(t, registry.RegisterField(
		adminschema.KindAdminRouteBinding,
		"extensions.owner",
		adminschema.FieldSchema{Type: adminschema.FieldTypeString},
	))
	service, err := NewRouteBindingService(store, registry)
	require.NoError(t, err)
	space, err := currentRouteBindingKeySpace()
	require.NoError(t, err)

	object := routeBindingTestObject("rollback-reconcile", "GetUser")
	_, err = service.SaveDraft(object)
	require.NoError(t, err)
	first, err := service.Publish("rollback-reconcile")
	require.NoError(t, err)
	changed := object.Clone()
	changed.Spec["extensions"] = map[string]any{"owner": "team-a"}
	_, err = service.SaveDraft(changed)
	require.NoError(t, err)
	second, err := service.Publish("rollback-reconcile")
	require.NoError(t, err)
	require.NoError(t, store.Delete(space.methodKey(second.ResourceID, second.MethodID)))

	rolledBack, err := service.Rollback("rollback-reconcile", first.Revision)
	require.NoError(t, err)
	assert.Equal(t, second.Revision+1, rolledBack.Revision)
	methodValue, exists := store.value(space.methodKey(rolledBack.ResourceID, rolledBack.MethodID))
	require.True(t, exists)
	assert.Equal(t, rolledBack.MethodYAML, methodValue)
}

func TestRouteBindingServiceRejectsBeforeAllocatingIDs(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)

	invalid := routeBindingTestObject("invalid", "GetUser")
	invalid.Spec["timeout"] = "0s"
	_, err := service.SaveDraft(invalid)
	require.Error(t, err)

	valid, err := service.SaveDraft(routeBindingTestObject("valid", "GetUser"))
	require.NoError(t, err)
	assert.Equal(t, 1, valid.ResourceID)
	assert.Equal(t, 1, valid.MethodID)
}

func TestRouteBindingServiceCanDeleteDraftWithoutPublishedProjection(t *testing.T) {
	installRouteBindingBootstrap(t)
	store := newMemoryRouteBindingStore()
	service := newRouteBindingServiceForTest(t, store)

	_, err := service.SaveDraft(routeBindingTestObject("temporary", "GetUser"))
	require.NoError(t, err)
	require.NoError(t, service.DeleteDraft("temporary"))
	_, err = service.Get("temporary")
	require.Error(t, err)
}

func routeBindingTestObject(name, targetMethod string) adminschema.AdminObject {
	return adminschema.AdminObject{
		Kind:     adminschema.KindAdminRouteBinding,
		Metadata: adminschema.ObjectMetadata{Name: name},
		Spec: map[string]any{
			"entry": map[string]any{
				"path":   "/api/v1/users/:id",
				"method": http.MethodGet,
			},
			"target": map[string]any{
				"application": "UserProvider",
				"interface":   "com.example.UserService",
				"method":      targetMethod,
				"cluster":     "user-dubbo",
			},
			"params": []any{
				map[string]any{
					"from": "uri.id",
					"to":   0,
					"type": "java.lang.String",
				},
			},
			"timeout": "2s",
		},
	}
}

func httpRequestForRouteTest(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	return request
}

func assertPublishedProjectionOrderAndRoute(
	t *testing.T,
	commit memoryRouteBindingCommit,
	space routeBindingKeySpace,
	record RouteBindingRecord,
) {
	t.Helper()

	discovery := apiconfig.NewLocalMemoryAPIDiscoveryService()
	replayRouteBindingRuntimeProjection(t, discovery, commit, space, record)
	matched, err := discovery.MatchAPI("/api/v1/users/42", http.MethodGet)
	require.NoError(t, err)
	assert.Equal(t, "GetUser", matched.Method.Method)
}

func replayRouteBindingRuntimeProjection(
	t *testing.T,
	discovery *apiconfig.LocalMemoryAPIDiscoveryService,
	commit memoryRouteBindingCommit,
	space routeBindingKeySpace,
	record RouteBindingRecord,
) {
	t.Helper()

	resourceKey := space.resourceKey(record.ResourceID)
	methodKey := space.methodKey(record.ResourceID, record.MethodID)
	runtimeKeys := make([]string, 0, 2)
	var resource config.Resource
	resourceSeen := false
	for _, put := range commit.puts {
		switch put.Key {
		case resourceKey:
			runtimeKeys = append(runtimeKeys, put.Key)
			require.NoError(t, yaml.UnmarshalYML([]byte(put.Value), &resource))
			resourceSeen = true
			assert.True(t, discovery.ResourceAdd(resource))
		case methodKey:
			runtimeKeys = append(runtimeKeys, put.Key)
			require.True(t, resourceSeen, "the resource event must precede its method event")
			var method config.Method
			require.NoError(t, yaml.UnmarshalYML([]byte(put.Value), &method))
			assert.True(t, discovery.MethodAdd(resource, method))
		}
	}

	assert.Equal(t, []string{resourceKey, methodKey}, runtimeKeys)
}

func routeBindingPutKeys(commit memoryRouteBindingCommit) []string {
	keys := make([]string, 0, len(commit.puts))
	for _, put := range commit.puts {
		keys = append(keys, put.Key)
	}
	return keys
}

var _ RouteBindingStore = (*memoryRouteBindingStore)(nil)
