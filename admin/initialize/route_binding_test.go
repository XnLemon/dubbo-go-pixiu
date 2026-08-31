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

package initialize

import (
	"testing"
)

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRoutersRegisterRouteBindingLifecycleEndpoints(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })

	router := Routers()
	routes := make(map[string]struct{}, len(router.Routes()))
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	for _, expected := range []string{
		"GET /config/api/route-binding/schema",
		"GET /config/api/route-binding/list",
		"GET /config/api/route-binding/detail",
		"POST /config/api/route-binding",
		"POST /config/api/route-binding/preview",
		"PUT /config/api/route-binding/publish",
		"GET /config/api/route-binding/history",
		"POST /config/api/route-binding/rollback",
		"DELETE /config/api/route-binding",
	} {
		assert.Contains(t, routes, expected)
	}
}
