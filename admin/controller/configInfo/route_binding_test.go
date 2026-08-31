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

package configInfo

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

import (
	adminconfig "github.com/apache/dubbo-go-pixiu/admin/config"
	"github.com/apache/dubbo-go-pixiu/admin/logic"
	"github.com/apache/dubbo-go-pixiu/pkg/config/schema"
)

type routeBindingManagerStub struct {
	savedObject       schema.AdminObject
	previewObject     schema.AdminObject
	previewSavedName  string
	previewResult     logic.RouteBindingPreview
	savedRecord       logic.RouteBindingRecord
	getResult         logic.RouteBindingDetail
	listResult        []logic.RouteBindingSummary
	publishResult     logic.RouteBindingRecord
	historyResult     []logic.RouteBindingRecord
	rollbackResult    logic.RouteBindingRecord
	deletedDraftName  string
	deleteDraftCalled bool
}

func (s *routeBindingManagerStub) SaveDraft(object schema.AdminObject) (logic.RouteBindingRecord, error) {
	s.savedObject = object
	return s.savedRecord, nil
}

func (s *routeBindingManagerStub) Preview(object schema.AdminObject) (logic.RouteBindingPreview, error) {
	s.previewObject = object
	return s.previewResult, nil
}

func (s *routeBindingManagerStub) PreviewSaved(name string) (logic.RouteBindingPreview, error) {
	s.previewSavedName = name
	return s.previewResult, nil
}

func (s *routeBindingManagerStub) Get(string) (logic.RouteBindingDetail, error) {
	return s.getResult, nil
}

func (s *routeBindingManagerStub) List() ([]logic.RouteBindingSummary, error) {
	return s.listResult, nil
}

func (s *routeBindingManagerStub) Publish(string) (logic.RouteBindingRecord, error) {
	return s.publishResult, nil
}

func (s *routeBindingManagerStub) History(string) ([]logic.RouteBindingRecord, error) {
	return s.historyResult, nil
}

func (s *routeBindingManagerStub) Rollback(string, uint64) (logic.RouteBindingRecord, error) {
	return s.rollbackResult, nil
}

func (s *routeBindingManagerStub) DeleteDraft(name string) error {
	s.deletedDraftName = name
	s.deleteDraftCalled = true
	return nil
}

func installRouteBindingManager(t *testing.T, manager logic.RouteBindingManager) {
	t.Helper()
	logic.SetRouteBindingManager(manager)
	t.Cleanup(func() { logic.SetRouteBindingManager(nil) })
}

func TestSaveRouteBindingDraftAcceptsJSONBody(t *testing.T) {
	manager := &routeBindingManagerStub{}
	installRouteBindingManager(t, manager)

	body := `{"kind":"AdminRouteBinding","metadata":{"name":"json-route"},"spec":{}}`
	recorder := callRouteBindingHandler(t, http.MethodPost, "/config/api/route-binding", strings.NewReader(body), "application/json", SaveRouteBindingDraft)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, adminconfig.OK, responseCode(t, recorder))
	assert.Equal(t, "json-route", manager.savedObject.Metadata.Name)
	assert.Equal(t, schema.KindAdminRouteBinding, manager.savedObject.Kind)
}

func TestSaveRouteBindingDraftAcceptsExistingFormContentConvention(t *testing.T) {
	manager := &routeBindingManagerStub{}
	installRouteBindingManager(t, manager)

	values := url.Values{}
	values.Set("content", "kind: AdminRouteBinding\nmetadata:\n  name: form-route\nspec: {}\n")
	recorder := callRouteBindingHandler(
		t,
		http.MethodPost,
		"/config/api/route-binding",
		strings.NewReader(values.Encode()),
		"application/x-www-form-urlencoded",
		SaveRouteBindingDraft,
	)

	assert.Equal(t, adminconfig.OK, responseCode(t, recorder))
	assert.Equal(t, "form-route", manager.savedObject.Metadata.Name)
}

func TestPreviewRouteBindingUsesBodyOrSavedName(t *testing.T) {
	manager := &routeBindingManagerStub{previewResult: logic.RouteBindingPreview{LegacyYAML: "name: preview"}}
	installRouteBindingManager(t, manager)

	body := `{"kind":"AdminRouteBinding","metadata":{"name":"candidate"},"spec":{}}`
	recorder := callRouteBindingHandler(t, http.MethodPost, "/config/api/route-binding/preview", strings.NewReader(body), "application/json", PreviewRouteBinding)
	assert.Equal(t, adminconfig.OK, responseCode(t, recorder))
	assert.Equal(t, "candidate", manager.previewObject.Metadata.Name)

	recorder = callRouteBindingHandler(t, http.MethodPost, "/config/api/route-binding/preview?name=saved-route", bytes.NewReader(nil), "", PreviewRouteBinding)
	assert.Equal(t, adminconfig.OK, responseCode(t, recorder))
	assert.Equal(t, "saved-route", manager.previewSavedName)
}

func TestRouteBindingHandlersReturnAdminErrorEnvelope(t *testing.T) {
	manager := &routeBindingManagerStub{}
	installRouteBindingManager(t, manager)

	recorder := callRouteBindingHandler(t, http.MethodPost, "/config/api/route-binding", strings.NewReader("not: [valid"), "application/json", SaveRouteBindingDraft)
	assert.Equal(t, adminconfig.ERR, responseCode(t, recorder))
	assert.Contains(t, responseData(t, recorder), "decode route binding content")
}

func TestRouteBindingSchemaEndpointUsesBuiltinRegistry(t *testing.T) {
	recorder := callRouteBindingHandler(t, http.MethodGet, "/config/api/route-binding/schema", nil, "", GetRouteBindingSchema)
	assert.Equal(t, adminconfig.OK, responseCode(t, recorder))
	var schemas []schema.ObjectSchema
	require.NoError(t, json.Unmarshal([]byte(responseData(t, recorder)), &schemas))
	require.Len(t, schemas, 1)
	assert.Equal(t, schema.KindAdminRouteBinding, schemas[0].Kind)
}

func callRouteBindingHandler(t *testing.T, method, target string, body io.Reader, contentType string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = req
	handler(context)
	return recorder
}

func responseCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response adminconfig.RetData
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	return response.Code
}

func responseData(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response adminconfig.RetData
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	data, ok := response.Data.(string)
	if !ok {
		encoded, err := json.Marshal(response.Data)
		require.NoError(t, err)
		return string(encoded)
	}
	return data
}

var _ logic.RouteBindingManager = (*routeBindingManagerStub)(nil)
