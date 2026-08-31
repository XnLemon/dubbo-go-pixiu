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
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

import (
	adminconfig "github.com/apache/dubbo-go-pixiu/admin/config"
	"github.com/apache/dubbo-go-pixiu/pkg/common/yaml"
	legacyconfig "github.com/apache/dubbo-go-pixiu/pkg/config"
	"github.com/apache/dubbo-go-pixiu/pkg/config/schema"
)

const (
	routeBindingRootName  = "admin/route-bindings/v1"
	routeBindingDrafts    = "drafts"
	routeBindingPublished = "published"
	routeBindingHistory   = "history"
)

var routeBindingNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// RouteBindingRecord is the durable representation of one draft or published
// route. ResourceYAML and MethodYAML are kept with the record so a rollback
// remains an exact runtime snapshot even if the compiler evolves later.
type RouteBindingRecord struct {
	Object       schema.AdminObject `json:"object" yaml:"object"`
	ResourceID   int                `json:"resourceId" yaml:"resourceId"`
	MethodID     int                `json:"methodId" yaml:"methodId"`
	Revision     uint64             `json:"revision" yaml:"revision"`
	UpdatedAt    time.Time          `json:"updatedAt" yaml:"updatedAt"`
	ResourceYAML string             `json:"resourceYaml" yaml:"resourceYaml"`
	MethodYAML   string             `json:"methodYaml" yaml:"methodYaml"`
	LegacyYAML   string             `json:"legacyYaml" yaml:"legacyYaml"`
}

// RouteBindingSummary is the list view returned to Admin.
type RouteBindingSummary struct {
	Name              string    `json:"name" yaml:"name"`
	Status            string    `json:"status" yaml:"status"`
	DraftRevision     uint64    `json:"draftRevision,omitempty" yaml:"draftRevision,omitempty"`
	PublishedRevision uint64    `json:"publishedRevision,omitempty" yaml:"publishedRevision,omitempty"`
	ResourceID        int       `json:"resourceId,omitempty" yaml:"resourceId,omitempty"`
	MethodID          int       `json:"methodId,omitempty" yaml:"methodId,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt" yaml:"updatedAt"`
}

// RouteBindingDetail contains both sides of the control-plane state. A
// published record may exist without a draft only when it was created by an
// older/manual path, so both fields are optional.
type RouteBindingDetail struct {
	Name      string              `json:"name" yaml:"name"`
	Draft     *RouteBindingRecord `json:"draft,omitempty" yaml:"draft,omitempty"`
	Published *RouteBindingRecord `json:"published,omitempty" yaml:"published,omitempty"`
}

// RouteBindingPreview is intentionally based on the generated legacy YAML.
// It is the same representation consumed by the current Pixiu runtime.
type RouteBindingPreview struct {
	Object              schema.AdminObject `json:"object" yaml:"object"`
	ResourceID          int                `json:"resourceId,omitempty" yaml:"resourceId,omitempty"`
	MethodID            int                `json:"methodId,omitempty" yaml:"methodId,omitempty"`
	LegacyYAML          string             `json:"legacyYaml" yaml:"legacyYaml"`
	PublishedLegacyYAML string             `json:"publishedLegacyYaml,omitempty" yaml:"publishedLegacyYaml,omitempty"`
	Diff                string             `json:"diff,omitempty" yaml:"diff,omitempty"`
	PublishedRevision   uint64             `json:"publishedRevision,omitempty" yaml:"publishedRevision,omitempty"`
}

// RouteBindingManager is the Admin-facing lifecycle contract. It deliberately
// models one binding at a time; a multi-object immutable ConfigSet can be
// introduced upstream after this vertical slice has proved the boundaries.
type RouteBindingManager interface {
	SaveDraft(object schema.AdminObject) (RouteBindingRecord, error)
	Preview(object schema.AdminObject) (RouteBindingPreview, error)
	PreviewSaved(name string) (RouteBindingPreview, error)
	Get(name string) (RouteBindingDetail, error)
	List() ([]RouteBindingSummary, error)
	Publish(name string) (RouteBindingRecord, error)
	History(name string) ([]RouteBindingRecord, error)
	Rollback(name string, revision uint64) (RouteBindingRecord, error)
	DeleteDraft(name string) error
}

// RouteBindingService is the minimal implementation behind the Admin REST
// handlers. Its mutex serializes local writers; etcd Commit makes the
// published legacy projection one transaction.
type RouteBindingService struct {
	store    RouteBindingStore
	registry *schema.Registry
	mu       sync.Mutex
}

func NewRouteBindingService(store RouteBindingStore, registry *schema.Registry) (*RouteBindingService, error) {
	if store == nil {
		return nil, fmt.Errorf("route binding store is nil")
	}
	if registry == nil {
		return nil, fmt.Errorf("route binding schema registry is nil")
	}
	return &RouteBindingService{store: store, registry: registry}, nil
}

var (
	routeBindingManagerMu sync.RWMutex
	routeBindingManager   RouteBindingManager
)

// GetRouteBindingManager returns the lazily-created production manager. The
// etcd client is opened by Admin startup and is deliberately reused here.
func GetRouteBindingManager() (RouteBindingManager, error) {
	routeBindingManagerMu.RLock()
	manager := routeBindingManager
	routeBindingManagerMu.RUnlock()
	if manager != nil {
		return manager, nil
	}

	routeBindingManagerMu.Lock()
	defer routeBindingManagerMu.Unlock()
	if routeBindingManager != nil {
		return routeBindingManager, nil
	}
	store, err := newEtcdRouteBindingStore()
	if err != nil {
		return nil, err
	}
	registry, err := schema.NewBuiltinRegistry()
	if err != nil {
		return nil, fmt.Errorf("create route binding schema registry: %w", err)
	}
	service, err := NewRouteBindingService(store, registry)
	if err != nil {
		return nil, err
	}
	routeBindingManager = service
	return routeBindingManager, nil
}

// SetRouteBindingManager replaces the process-local manager. It is useful for
// embedding Admin with another storage implementation and for controller
// tests; normal startup should use GetRouteBindingManager.
func SetRouteBindingManager(manager RouteBindingManager) {
	routeBindingManagerMu.Lock()
	routeBindingManager = manager
	routeBindingManagerMu.Unlock()
}

type routeBindingKeySpace struct {
	base            string
	root            string
	drafts          string
	published       string
	history         string
	resourceCounter string
	methodCounter   string
	legacyResources string
}

func currentRouteBindingKeySpace() (routeBindingKeySpace, error) {
	if adminconfig.Bootstrap == nil {
		return routeBindingKeySpace{}, fmt.Errorf("admin bootstrap is not initialized")
	}
	base := strings.TrimRight(strings.TrimSpace(adminconfig.Bootstrap.GetPath()), "/")
	if base == "" {
		return routeBindingKeySpace{}, fmt.Errorf("admin etcd path is empty")
	}
	root := base + "/" + routeBindingRootName
	return routeBindingKeySpace{
		base:            base,
		root:            root,
		drafts:          root + "/" + routeBindingDrafts + "/",
		published:       root + "/" + routeBindingPublished + "/",
		history:         root + "/" + routeBindingHistory + "/",
		resourceCounter: base + "/resourceId",
		methodCounter:   base + "/methodId",
		legacyResources: base + "/resources/",
	}, nil
}

func (k routeBindingKeySpace) draftKey(name string) string {
	return k.drafts + name
}

func (k routeBindingKeySpace) publishedKey(name string) string {
	return k.published + name
}

func (k routeBindingKeySpace) historyPrefix(name string) string {
	return k.history + name + "/"
}

func (k routeBindingKeySpace) historyKey(name string, revision uint64) string {
	return k.historyPrefix(name) + fmt.Sprintf("%020d", revision)
}

func (k routeBindingKeySpace) resourceKey(id int) string {
	return k.legacyResources + fmt.Sprintf("%d", id)
}

func (k routeBindingKeySpace) methodKey(resourceID, methodID int) string {
	return k.resourceKey(resourceID) + "/method/" + fmt.Sprintf("%d", methodID)
}

func validateRouteBindingName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("metadata.name is required")
	}
	if trimmed != name {
		return "", fmt.Errorf("metadata.name must not contain leading or trailing whitespace")
	}
	if !routeBindingNamePattern.MatchString(name) {
		return "", fmt.Errorf("metadata.name %q must contain only letters, numbers, '.', '_' or '-'", name)
	}
	return name, nil
}

func (s *RouteBindingService) SaveDraft(object schema.AdminObject) (RouteBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveDraftLocked(object)
}

func (s *RouteBindingService) saveDraftLocked(object schema.AdminObject) (RouteBindingRecord, error) {
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingRecord{}, err
	}
	name, err := validateRouteBindingName(object.Metadata.Name)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	// Compile before allocating IDs so a rejected object cannot consume a
	// legacy resource or method ID.
	if _, err = s.compileRecord(object, 0, 0, "draft"); err != nil {
		return RouteBindingRecord{}, err
	}
	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}

	resourceID, methodID := recordIDs(published, publishedExists)
	if !publishedExists {
		resourceID, methodID = recordIDs(draft, draftExists)
		resourceID, methodID, err = s.ensureUnpublishedRuntimeIDs(space, resourceID, methodID)
		if err != nil {
			return RouteBindingRecord{}, err
		}
	}

	record, err := s.compileRecord(object, resourceID, methodID, "draft")
	if err != nil {
		return RouteBindingRecord{}, err
	}
	record.Revision = nextDraftRevision(draft, draftExists, published, publishedExists)
	record.UpdatedAt = time.Now().UTC()
	encoded, err := encodeRouteBindingRecord(record)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	if err = s.store.Put(space.draftKey(name), encoded); err != nil {
		return RouteBindingRecord{}, fmt.Errorf("save route binding draft %q: %w", name, err)
	}
	return record, nil
}

func (s *RouteBindingService) Preview(object schema.AdminObject) (RouteBindingPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewLocked(object)
}

func (s *RouteBindingService) PreviewSaved(name string) (RouteBindingPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingPreview{}, err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return RouteBindingPreview{}, err
	}
	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingPreview{}, err
	}
	if draftExists {
		return s.previewLocked(draft.Object)
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingPreview{}, err
	}
	if !publishedExists {
		return RouteBindingPreview{}, fmt.Errorf("route binding %q was not found", name)
	}
	return s.previewLocked(published.Object)
}

func (s *RouteBindingService) previewLocked(object schema.AdminObject) (RouteBindingPreview, error) {
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingPreview{}, err
	}
	name, err := validateRouteBindingName(object.Metadata.Name)
	if err != nil {
		return RouteBindingPreview{}, err
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingPreview{}, err
	}
	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingPreview{}, err
	}
	resourceID, methodID := recordIDs(draft, draftExists)
	if resourceID == 0 || methodID == 0 {
		resourceID, methodID = recordIDs(published, publishedExists)
	}
	candidate, err := s.compileRecord(object, resourceID, methodID, "draft")
	if err != nil {
		return RouteBindingPreview{}, err
	}
	result := RouteBindingPreview{
		Object:     candidate.Object,
		ResourceID: candidate.ResourceID,
		MethodID:   candidate.MethodID,
		LegacyYAML: candidate.LegacyYAML,
	}
	if publishedExists {
		result.PublishedLegacyYAML = published.LegacyYAML
		result.PublishedRevision = published.Revision
		result.Diff = unifiedRouteBindingDiff(published.LegacyYAML, candidate.LegacyYAML)
	}
	return result, nil
}

func (s *RouteBindingService) Get(name string) (RouteBindingDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingDetail{}, err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return RouteBindingDetail{}, err
	}
	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingDetail{}, err
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingDetail{}, err
	}
	if !draftExists && !publishedExists {
		return RouteBindingDetail{}, fmt.Errorf("route binding %q was not found", name)
	}
	detail := RouteBindingDetail{Name: name}
	if draftExists {
		draftCopy := draft
		detail.Draft = &draftCopy
	}
	if publishedExists {
		publishedCopy := published
		detail.Published = &publishedCopy
	}
	return detail, nil
}

func (s *RouteBindingService) List() ([]RouteBindingSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return nil, err
	}
	drafts, err := s.listRecords(space.drafts)
	if err != nil {
		return nil, err
	}
	published, err := s.listRecords(space.published)
	if err != nil {
		return nil, err
	}
	draftByName := make(map[string]RouteBindingRecord, len(drafts))
	publishedByName := make(map[string]RouteBindingRecord, len(published))
	for _, record := range drafts {
		name := record.Object.Metadata.Name
		draftByName[name] = record
	}
	for _, record := range published {
		name := record.Object.Metadata.Name
		publishedByName[name] = record
	}
	names := make(map[string]struct{}, len(draftByName)+len(publishedByName))
	for name := range draftByName {
		names[name] = struct{}{}
	}
	for name := range publishedByName {
		names[name] = struct{}{}
	}
	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)
	result := make([]RouteBindingSummary, 0, len(orderedNames))
	for _, name := range orderedNames {
		draft, hasDraft := draftByName[name]
		published, hasPublished := publishedByName[name]
		summary := RouteBindingSummary{Name: name}
		switch {
		case hasDraft && !hasPublished:
			summary.Status = "draft"
			summary.DraftRevision = draft.Revision
			summary.ResourceID, summary.MethodID = recordIDs(draft, true)
			summary.UpdatedAt = draft.UpdatedAt
		case !hasDraft && hasPublished:
			summary.Status = "published"
			summary.PublishedRevision = published.Revision
			summary.ResourceID, summary.MethodID = recordIDs(published, true)
			summary.UpdatedAt = published.UpdatedAt
		default:
			summary.DraftRevision = draft.Revision
			summary.PublishedRevision = published.Revision
			summary.ResourceID, summary.MethodID = recordIDs(draft, true)
			if sameRouteBindingState(draft, published) {
				summary.Status = "published"
			} else {
				summary.Status = "modified"
			}
			if draft.UpdatedAt.After(published.UpdatedAt) {
				summary.UpdatedAt = draft.UpdatedAt
			} else {
				summary.UpdatedAt = published.UpdatedAt
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func (s *RouteBindingService) Publish(name string) (RouteBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingRecord{}, err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	if !draftExists {
		return RouteBindingRecord{}, fmt.Errorf("route binding draft %q was not found", name)
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	resourceID, methodID := recordIDs(published, publishedExists)
	if !publishedExists {
		resourceID, methodID = recordIDs(draft, true)
		resourceID, methodID, err = s.ensureUnpublishedRuntimeIDs(space, resourceID, methodID)
		if err != nil {
			return RouteBindingRecord{}, err
		}
	}
	record, err := s.compileRecord(draft.Object, resourceID, methodID, "published")
	if err != nil {
		return RouteBindingRecord{}, err
	}
	runtimePuts, err := s.planRuntimeProjection(space, name, record, published, publishedExists)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	if publishedExists && sameRouteBindingState(published, record) {
		if len(runtimePuts) > 0 {
			if err = s.store.Commit(runtimePuts, nil); err != nil {
				return RouteBindingRecord{}, fmt.Errorf("reconcile route binding %q runtime projection: %w", name, err)
			}
		}
		return published, nil
	}
	record.Revision = nextPublishedRevision(published, publishedExists)
	record.UpdatedAt = time.Now().UTC()
	var draftUpdate *RouteBindingRecord
	if draft.ResourceID != record.ResourceID || draft.MethodID != record.MethodID {
		updatedDraft, compileErr := s.compileRecord(draft.Object, record.ResourceID, record.MethodID, "draft")
		if compileErr != nil {
			return RouteBindingRecord{}, compileErr
		}
		updatedDraft.Revision = draft.Revision
		updatedDraft.UpdatedAt = draft.UpdatedAt
		draftUpdate = &updatedDraft
	}
	if err = s.commitPublished(space, name, record, published, publishedExists, runtimePuts, draftUpdate); err != nil {
		return RouteBindingRecord{}, err
	}
	return record, nil
}

func (s *RouteBindingService) History(name string) ([]RouteBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return nil, err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return nil, err
	}
	records, err := s.listRecords(space.historyPrefix(name))
	if err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Revision > records[j].Revision })
	return records, nil
}

func (s *RouteBindingService) Rollback(name string, revision uint64) (RouteBindingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return RouteBindingRecord{}, err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	published, publishedExists, err := s.loadRecord(space.publishedKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	if !publishedExists {
		return RouteBindingRecord{}, fmt.Errorf("route binding %q has no published revision", name)
	}
	history, err := s.listRecords(space.historyPrefix(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	target, found := chooseRollbackTarget(history, published.Revision, revision)
	if !found {
		if revision == 0 {
			return RouteBindingRecord{}, fmt.Errorf("route binding %q has no previous revision", name)
		}
		return RouteBindingRecord{}, fmt.Errorf("route binding %q revision %d was not found", name, revision)
	}
	candidate, err := s.rollbackRecord(target, published)
	if err != nil {
		return RouteBindingRecord{}, fmt.Errorf("prepare rollback revision %d: %w", target.Revision, err)
	}
	candidate.Revision = nextPublishedRevision(published, true)
	candidate.UpdatedAt = time.Now().UTC()
	runtimePuts, err := s.planRuntimeProjection(space, name, candidate, published, true)
	if err != nil {
		return RouteBindingRecord{}, err
	}

	draft, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return RouteBindingRecord{}, err
	}
	draftCandidate := candidate
	draftCandidate.Object = withPublishMode(target.Object, "draft")
	draftCandidate.Revision = nextDraftRevision(draft, draftExists, candidate, true)
	draftCandidate.UpdatedAt = candidate.UpdatedAt
	if err = s.commitPublished(space, name, candidate, published, true, runtimePuts, &draftCandidate); err != nil {
		return RouteBindingRecord{}, err
	}
	return candidate, nil
}

func (s *RouteBindingService) rollbackRecord(target, published RouteBindingRecord) (RouteBindingRecord, error) {
	resourceID, methodID := recordIDs(published, true)
	if target.ResourceID == resourceID &&
		target.MethodID == methodID &&
		strings.TrimSpace(target.ResourceYAML) != "" &&
		strings.TrimSpace(target.MethodYAML) != "" &&
		strings.TrimSpace(target.LegacyYAML) != "" {
		target.Object = withPublishMode(target.Object, "published")
		return target, nil
	}

	// Records created before runtime snapshots were stored still remain
	// rollback-compatible, but must be projected by the current compiler.
	return s.compileRecord(target.Object, resourceID, methodID, "published")
}

func (s *RouteBindingService) DeleteDraft(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	space, err := currentRouteBindingKeySpace()
	if err != nil {
		return err
	}
	name, err = validateRouteBindingName(name)
	if err != nil {
		return err
	}
	_, draftExists, err := s.loadRecord(space.draftKey(name))
	if err != nil {
		return err
	}
	if !draftExists {
		return fmt.Errorf("route binding draft %q was not found", name)
	}
	if err = s.store.Delete(space.draftKey(name)); err != nil {
		return fmt.Errorf("delete route binding draft %q: %w", name, err)
	}
	return nil
}

func (s *RouteBindingService) compileRecord(object schema.AdminObject, resourceID, methodID int, publishMode string) (RouteBindingRecord, error) {
	normalized, err := s.registry.Normalize(object)
	if err != nil {
		return RouteBindingRecord{}, fmt.Errorf("validate route binding: %w", err)
	}
	normalized.Metadata.Name, err = validateRouteBindingName(normalized.Metadata.Name)
	if err != nil {
		return RouteBindingRecord{}, err
	}
	normalized = withPublishMode(normalized, publishMode)
	compiled, err := schema.CompileAdminRouteBinding(s.registry, normalized)
	if err != nil {
		return RouteBindingRecord{}, fmt.Errorf("compile route binding %q: %w", normalized.Metadata.Name, err)
	}
	compiled.Resource.ID = resourceID
	compiled.Method.ID = methodID
	resourceYAML, err := yaml.MarshalYML(compiled.Resource)
	if err != nil {
		return RouteBindingRecord{}, fmt.Errorf("encode route binding resource: %w", err)
	}
	methodYAML, err := yaml.MarshalYML(compiled.Method)
	if err != nil {
		return RouteBindingRecord{}, fmt.Errorf("encode route binding method: %w", err)
	}
	legacyYAML, err := compiled.PreviewYAML()
	if err != nil {
		return RouteBindingRecord{}, err
	}
	return RouteBindingRecord{
		Object:       compiled.Source,
		ResourceID:   resourceID,
		MethodID:     methodID,
		ResourceYAML: string(resourceYAML),
		MethodYAML:   string(methodYAML),
		LegacyYAML:   string(legacyYAML),
	}, nil
}

func (s *RouteBindingService) commitPublished(
	space routeBindingKeySpace,
	name string,
	record, previous RouteBindingRecord,
	previousExists bool,
	runtimePuts []RouteBindingKV,
	draft *RouteBindingRecord,
) error {
	encoded, err := encodeRouteBindingRecord(record)
	if err != nil {
		return err
	}
	historyEncoded, err := encodeRouteBindingRecord(record)
	if err != nil {
		return err
	}
	puts := []RouteBindingKV{
		{Key: space.publishedKey(name), Value: encoded},
		{Key: space.historyKey(name, record.Revision), Value: historyEncoded},
	}
	puts = append(puts, runtimePuts...)
	if draft != nil {
		draftEncoded, encodeErr := encodeRouteBindingRecord(*draft)
		if encodeErr != nil {
			return encodeErr
		}
		puts = append(puts, RouteBindingKV{Key: space.draftKey(name), Value: draftEncoded})
	}
	deletes := make([]string, 0, 2)
	if previousExists {
		if previous.ResourceID > 0 && previous.ResourceID != record.ResourceID {
			deletes = append(deletes,
				space.resourceKey(previous.ResourceID),
				space.methodKey(previous.ResourceID, previous.MethodID),
			)
		} else if previous.MethodID > 0 && previous.MethodID != record.MethodID {
			deletes = append(deletes, space.methodKey(record.ResourceID, previous.MethodID))
		}
	}
	if err = s.store.Commit(puts, deletes); err != nil {
		return fmt.Errorf("publish route binding %q: %w", name, err)
	}
	return nil
}

func (s *RouteBindingService) ensureUnpublishedRuntimeIDs(
	space routeBindingKeySpace,
	resourceID, methodID int,
) (int, int, error) {
	var err error
	if resourceID > 0 {
		_, occupied, getErr := s.store.Get(space.resourceKey(resourceID))
		if getErr != nil {
			return 0, 0, fmt.Errorf("check route binding resource id %d: %w", resourceID, getErr)
		}
		if occupied {
			resourceID = 0
		}
	}
	if resourceID == 0 {
		resourceID, err = s.nextAvailableRuntimeID(
			space.resourceCounter,
			space.resourceKey,
			"resource",
		)
		if err != nil {
			return 0, 0, err
		}
	}

	if methodID > 0 {
		_, occupied, getErr := s.store.Get(space.methodKey(resourceID, methodID))
		if getErr != nil {
			return 0, 0, fmt.Errorf("check route binding method id %d: %w", methodID, getErr)
		}
		if occupied {
			methodID = 0
		}
	}
	if methodID == 0 {
		methodID, err = s.nextAvailableRuntimeID(
			space.methodCounter,
			func(id int) string { return space.methodKey(resourceID, id) },
			"method",
		)
		if err != nil {
			return 0, 0, err
		}
	}
	return resourceID, methodID, nil
}

func (s *RouteBindingService) nextAvailableRuntimeID(
	counterKey string,
	runtimeKey func(int) string,
	kind string,
) (int, error) {
	for attempt := 0; attempt < 100; attempt++ {
		id, err := s.store.NextID(counterKey)
		if err != nil {
			return 0, fmt.Errorf("allocate route binding %s id: %w", kind, err)
		}
		_, occupied, err := s.store.Get(runtimeKey(id))
		if err != nil {
			return 0, fmt.Errorf("check route binding %s id %d: %w", kind, id, err)
		}
		if !occupied {
			return id, nil
		}
	}
	return 0, fmt.Errorf("allocate route binding %s id: no free legacy key after 100 attempts", kind)
}

func (s *RouteBindingService) planRuntimeProjection(
	space routeBindingKeySpace,
	name string,
	candidate, previous RouteBindingRecord,
	previousExists bool,
) ([]RouteBindingKV, error) {
	resourceKey := space.resourceKey(candidate.ResourceID)
	methodKey := space.methodKey(candidate.ResourceID, candidate.MethodID)
	resourceOwned := previousExists && previous.ResourceID == candidate.ResourceID
	methodOwned := resourceOwned && previous.MethodID == candidate.MethodID

	puts := make([]RouteBindingKV, 0, 2)
	resourcePut, err := s.planRuntimeKey(resourceKey, candidate.ResourceYAML, resourceOwned)
	if err != nil {
		return nil, fmt.Errorf("route binding %q resource projection: %w", name, err)
	}
	if resourcePut != nil {
		puts = append(puts, *resourcePut)
	}
	methodPut, err := s.planRuntimeKey(methodKey, candidate.MethodYAML, methodOwned)
	if err != nil {
		return nil, fmt.Errorf("route binding %q method projection: %w", name, err)
	}
	if methodPut != nil {
		puts = append(puts, *methodPut)
	} else if resourcePut != nil {
		// A resource PUT reaches the runtime before its method keys. Replaying the
		// unchanged method makes a repaired resource immediately routable again.
		puts = append(puts, RouteBindingKV{Key: methodKey, Value: candidate.MethodYAML})
	}

	candidatePath := routeBindingPath(candidate.Object)
	candidateIdentity := runtimeRoutePathIdentity(candidatePath)
	items, err := s.store.List(space.legacyResources)
	if err != nil {
		return nil, fmt.Errorf("check route binding %q path conflicts: %w", name, err)
	}
	for _, item := range items {
		if !isLegacyResourceKey(space.legacyResources, item.Key) {
			continue
		}
		if item.Key == resourceKey {
			if resourceOwned {
				continue
			}
			return nil, fmt.Errorf("route binding %q legacy resource key %q is already owned by another configuration", name, item.Key)
		}
		var resource legacyconfig.Resource
		if err = yaml.UnmarshalYML([]byte(item.Value), &resource); err != nil {
			return nil, fmt.Errorf("check route binding %q against legacy resource %q: %w", name, item.Key, err)
		}
		if runtimeRoutePathIdentity(resource.Path) == candidateIdentity {
			return nil, fmt.Errorf(
				"route binding %q path %q conflicts with legacy resource %q path %q",
				name,
				candidatePath,
				item.Key,
				resource.Path,
			)
		}
	}
	return puts, nil
}

func (s *RouteBindingService) planRuntimeKey(key, value string, owned bool) (*RouteBindingKV, error) {
	current, exists, err := s.store.Get(key)
	if err != nil {
		return nil, err
	}
	if exists && !owned {
		return nil, fmt.Errorf("legacy key %q is already owned by another configuration", key)
	}
	if exists && current == value {
		return nil, nil
	}
	return &RouteBindingKV{Key: key, Value: value}, nil
}

func isLegacyResourceKey(prefix, key string) bool {
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	relative := strings.TrimPrefix(key, prefix)
	return relative != "" && !strings.Contains(relative, "/")
}

func routeBindingPath(object schema.AdminObject) string {
	entry, _ := object.Spec["entry"].(map[string]any)
	path, _ := entry["path"].(string)
	return path
}

// runtimeRoutePathIdentity mirrors the legacy router's case-insensitive path
// key and its shared trie node for named and wildcard path parameters.
func runtimeRoutePathIdentity(path string) string {
	identity := strings.ToLower(path)
	if strings.HasSuffix(identity, "/") {
		identity = strings.TrimSuffix(identity, "/")
	}
	segments := strings.Split(identity, "/")
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") || segment == "*" {
			segments[index] = ":"
		}
	}
	return strings.Join(segments, "/")
}

func sameRouteBindingState(left, right RouteBindingRecord) bool {
	return sameRuntimeProjection(left, right) && reflect.DeepEqual(
		withPublishMode(left.Object, "published"),
		withPublishMode(right.Object, "published"),
	)
}

func sameRuntimeProjection(left, right RouteBindingRecord) bool {
	return left.ResourceID == right.ResourceID &&
		left.MethodID == right.MethodID &&
		left.ResourceYAML == right.ResourceYAML &&
		left.MethodYAML == right.MethodYAML &&
		left.LegacyYAML == right.LegacyYAML
}

func (s *RouteBindingService) loadRecord(key string) (RouteBindingRecord, bool, error) {
	value, exists, err := s.store.Get(key)
	if err != nil {
		return RouteBindingRecord{}, false, err
	}
	if !exists {
		return RouteBindingRecord{}, false, nil
	}
	record, err := decodeRouteBindingRecord(value)
	if err != nil {
		return RouteBindingRecord{}, false, fmt.Errorf("decode route binding record %q: %w", key, err)
	}
	return record, true, nil
}

func (s *RouteBindingService) listRecords(prefix string) ([]RouteBindingRecord, error) {
	items, err := s.store.List(prefix)
	if err != nil {
		return nil, err
	}
	records := make([]RouteBindingRecord, 0, len(items))
	for _, item := range items {
		record, decodeErr := decodeRouteBindingRecord(item.Value)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode route binding record %q: %w", item.Key, decodeErr)
		}
		records = append(records, record)
	}
	return records, nil
}

func encodeRouteBindingRecord(record RouteBindingRecord) (string, error) {
	record.Object = record.Object.Clone()
	data, err := json.Marshal(record)
	if err != nil {
		return "", fmt.Errorf("encode route binding record: %w", err)
	}
	return string(data), nil
}

func decodeRouteBindingRecord(value string) (RouteBindingRecord, error) {
	var record RouteBindingRecord
	if err := json.Unmarshal([]byte(value), &record); err != nil {
		return RouteBindingRecord{}, err
	}
	return record, nil
}

func recordIDs(record RouteBindingRecord, exists bool) (int, int) {
	if !exists {
		return 0, 0
	}
	return record.ResourceID, record.MethodID
}

func nextDraftRevision(draft RouteBindingRecord, draftExists bool, published RouteBindingRecord, publishedExists bool) uint64 {
	var current uint64
	if draftExists && draft.Revision > current {
		current = draft.Revision
	}
	if publishedExists && published.Revision > current {
		current = published.Revision
	}
	return current + 1
}

func nextPublishedRevision(published RouteBindingRecord, exists bool) uint64 {
	if !exists {
		return 1
	}
	return published.Revision + 1
}

func chooseRollbackTarget(history []RouteBindingRecord, currentRevision, requested uint64) (RouteBindingRecord, bool) {
	var target RouteBindingRecord
	found := false
	for _, record := range history {
		if record.Revision >= currentRevision {
			continue
		}
		if requested != 0 {
			if record.Revision == requested {
				return record, true
			}
			continue
		}
		if !found || record.Revision > target.Revision {
			target = record
			found = true
		}
	}
	return target, found
}

func withPublishMode(object schema.AdminObject, mode string) schema.AdminObject {
	result := object.Clone()
	if result.Spec == nil {
		result.Spec = make(map[string]any)
	}
	publish, ok := result.Spec["publish"].(map[string]any)
	if !ok {
		publish = make(map[string]any)
	}
	publish["mode"] = mode
	result.Spec["publish"] = publish
	return result
}

func unifiedRouteBindingDiff(oldValue, newValue string) string {
	oldValue = normalizeDiffText(oldValue)
	newValue = normalizeDiffText(newValue)
	if oldValue == newValue {
		return ""
	}
	oldLines := diffLines(oldValue)
	newLines := diffLines(newValue)
	var builder strings.Builder
	builder.WriteString("--- published\n+++ candidate\n")
	if len(oldLines)*len(newLines) > 1_000_000 {
		for _, line := range oldLines {
			builder.WriteString("-")
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
		for _, line := range newLines {
			builder.WriteString("+")
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
		return builder.String()
	}
	common := make([][]int, len(oldLines)+1)
	for i := range common {
		common[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else if common[i+1][j] >= common[i][j+1] {
				common[i][j] = common[i+1][j]
			} else {
				common[i][j] = common[i][j+1]
			}
		}
	}
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			builder.WriteString(" ")
			builder.WriteString(oldLines[i])
			builder.WriteByte('\n')
			i++
			j++
			continue
		}
		if i < len(oldLines) && (j == len(newLines) || common[i+1][j] >= common[i][j+1]) {
			builder.WriteString("-")
			builder.WriteString(oldLines[i])
			builder.WriteByte('\n')
			i++
			continue
		}
		if j < len(newLines) {
			builder.WriteString("+")
			builder.WriteString(newLines[j])
			builder.WriteByte('\n')
			j++
		}
	}
	return builder.String()
}

func normalizeDiffText(value string) string {
	return strings.TrimSuffix(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
}

func diffLines(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

var _ RouteBindingManager = (*RouteBindingService)(nil)
