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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

import (
	"github.com/gin-gonic/gin"
)

import (
	adminconfig "github.com/apache/dubbo-go-pixiu/admin/config"
	"github.com/apache/dubbo-go-pixiu/admin/logic"
	"github.com/apache/dubbo-go-pixiu/pkg/config/schema"
)

const routeBindingContentLimit = 2 << 20

// GetRouteBindingSchema returns the schema used by the route-binding editor.
// The endpoint is deliberately backed by the same builtin registry used by
// the compiler, so form hints and server-side validation cannot drift apart.
//
// @Tags Config
// @Summary get the Admin route-binding schema
// @Produce application/json
// @Success 200 {object} string
// @Router /config/api/route-binding/schema [get]
func GetRouteBindingSchema(c *gin.Context) {
	registry, err := schema.NewBuiltinRegistry()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(registry.List()))
}

// ListRouteBindings returns the draft/published state summary for each
// high-level binding.
//
// @Tags Config
// @Summary list Admin route bindings
// @Produce application/json
// @Success 200 {object} string
// @Router /config/api/route-binding/list [get]
func ListRouteBindings(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.List()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// GetRouteBindingDetail returns the durable draft and published records for a
// binding.
//
// @Tags Config
// @Summary get an Admin route-binding detail
// @Produce application/json
// @Param name query string true "Route-binding name"
// @Success 200 {object} string
// @Router /config/api/route-binding/detail [get]
func GetRouteBindingDetail(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	name, err := routeBindingName(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.Get(name)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// SaveRouteBindingDraft validates and persists a draft. The request accepts
// the existing Admin form convention (content form field) as well as a raw
// JSON or YAML request body.
//
// @Tags Config
// @Summary save an Admin route-binding draft
// @Accept application/json
// @Accept application/x-yaml
// @Accept application/x-www-form-urlencoded
// @Produce application/json
// @Param content formData string false "AdminRouteBinding JSON/YAML"
// @Success 200 {object} string
// @Router /config/api/route-binding [post]
func SaveRouteBindingDraft(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	object, err := decodeRouteBindingObject(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.SaveDraft(object)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// PreviewRouteBinding compiles a candidate without changing either the draft
// store or the legacy runtime projection. If no body is supplied, name can be
// used to preview the currently saved draft (or published record).
//
// @Tags Config
// @Summary preview an Admin route binding
// @Accept application/json
// @Accept application/x-yaml
// @Accept application/x-www-form-urlencoded
// @Produce application/json
// @Param name query string false "Saved route-binding name"
// @Param content formData string false "AdminRouteBinding JSON/YAML"
// @Success 200 {object} string
// @Router /config/api/route-binding/preview [post]
func PreviewRouteBinding(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	content, contentErr := readRouteBindingContent(c)
	if contentErr != nil {
		writeRouteBindingError(c, contentErr)
		return
	}
	var result logic.RouteBindingPreview
	if strings.TrimSpace(content) == "" {
		name, nameErr := routeBindingName(c)
		if nameErr != nil {
			writeRouteBindingError(c, fmt.Errorf("preview content is required: %w", nameErr))
			return
		}
		result, err = manager.PreviewSaved(name)
	} else {
		object, decodeErr := decodeRouteBindingObjectContent(content)
		if decodeErr != nil {
			writeRouteBindingError(c, decodeErr)
			return
		}
		result, err = manager.Preview(object)
	}
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// PublishRouteBinding atomically promotes a saved draft to the legacy runtime
// keys and records a history revision.
//
// @Tags Config
// @Summary publish an Admin route binding
// @Produce application/json
// @Param name query string true "Route-binding name"
// @Success 200 {object} string
// @Router /config/api/route-binding/publish [put]
func PublishRouteBinding(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	name, err := routeBindingName(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.Publish(name)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// GetRouteBindingHistory returns published snapshots newest first.
//
// @Tags Config
// @Summary get route-binding publish history
// @Produce application/json
// @Param name query string true "Route-binding name"
// @Success 200 {object} string
// @Router /config/api/route-binding/history [get]
func GetRouteBindingHistory(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	name, err := routeBindingName(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.History(name)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// RollbackRouteBinding republishes a historical snapshot. revision=0 means
// the immediately previous published revision.
//
// @Tags Config
// @Summary rollback an Admin route binding
// @Accept application/json
// @Accept application/x-www-form-urlencoded
// @Produce application/json
// @Param name query string true "Route-binding name"
// @Param revision query uint false "Historical revision; zero means previous"
// @Success 200 {object} string
// @Router /config/api/route-binding/rollback [post]
func RollbackRouteBinding(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	name, err := routeBindingName(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	revision, err := routeBindingRevision(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	result, err := manager.Rollback(name, revision)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet(result))
}

// DeleteRouteBindingDraft removes the saved draft. If a published projection
// exists, it is retained so deleting an unpublished edit is a safe discard.
//
// @Tags Config
// @Summary delete an unpublished route-binding draft
// @Produce application/json
// @Param name query string true "Route-binding name"
// @Success 200 {object} string
// @Router /config/api/route-binding [delete]
func DeleteRouteBindingDraft(c *gin.Context) {
	manager, err := routeBindingManager()
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	name, err := routeBindingName(c)
	if err != nil {
		writeRouteBindingError(c, err)
		return
	}
	if err = manager.DeleteDraft(name); err != nil {
		writeRouteBindingError(c, err)
		return
	}
	c.JSON(http.StatusOK, adminconfig.WithRet("success"))
}

func routeBindingManager() (logic.RouteBindingManager, error) {
	return logic.GetRouteBindingManager()
}

func routeBindingName(c *gin.Context) (string, error) {
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		name = strings.TrimSpace(c.PostForm("name"))
	}
	if name == "" {
		return "", fmt.Errorf("route binding name is required")
	}
	return name, nil
}

func routeBindingRevision(c *gin.Context) (uint64, error) {
	raw := strings.TrimSpace(c.Query("revision"))
	if raw == "" {
		raw = strings.TrimSpace(c.PostForm("revision"))
	}
	if raw == "" {
		return 0, nil
	}
	revision, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("route binding revision %q is invalid", raw)
	}
	return revision, nil
}

func decodeRouteBindingObject(c *gin.Context) (schema.AdminObject, error) {
	content, err := readRouteBindingContent(c)
	if err != nil {
		return schema.AdminObject{}, err
	}
	return decodeRouteBindingObjectContent(content)
}

func readRouteBindingContent(c *gin.Context) (string, error) {
	if content := c.PostForm("content"); strings.TrimSpace(content) != "" {
		return content, nil
	}
	if c.Request.Body == nil {
		return "", fmt.Errorf("route binding content is required")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, routeBindingContentLimit+1))
	if err != nil {
		return "", fmt.Errorf("read route binding content: %w", err)
	}
	if len(body) > routeBindingContentLimit {
		return "", fmt.Errorf("route binding content exceeds %d bytes", routeBindingContentLimit)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", nil
	}
	return string(body), nil
}

func decodeRouteBindingObjectContent(content string) (schema.AdminObject, error) {
	object, err := schema.DecodeAdminObjectYAML([]byte(content))
	if err != nil {
		return schema.AdminObject{}, fmt.Errorf("decode route binding content: %w", err)
	}
	return object, nil
}

func writeRouteBindingError(c *gin.Context, err error) {
	if err == nil {
		err = fmt.Errorf("route binding request failed")
	}
	c.JSON(http.StatusOK, adminconfig.WithError(err))
}
