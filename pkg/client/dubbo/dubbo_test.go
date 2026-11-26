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

package dubbo

import (
	stderrors "errors"
	"fmt"
	"net"
	"testing"
)

import (
	perrors "github.com/pkg/errors"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeDubboCallError(t *testing.T) {
	rootCause := stderrors.New(dubboGettyReadTimeoutMessage)
	tests := []struct {
		name string
		err  error
		root error
	}{
		{
			name: "pkg errors new",
			err:  perrors.New(dubboGettyReadTimeoutMessage),
		},
		{
			name: "pkg errors with stack",
			err:  perrors.WithStack(perrors.New(dubboGettyReadTimeoutMessage)),
		},
		{
			name: "pkg errors wrap",
			err:  perrors.Wrap(rootCause, "cluster"),
			root: rootCause,
		},
		{
			name: "pkg errors with message",
			err:  perrors.WithMessage(rootCause, "cluster"),
			root: rootCause,
		},
		{
			name: "fmt wraps pkg errors with stack",
			err:  fmt.Errorf("cluster: %w", perrors.WithStack(rootCause)),
			root: rootCause,
		},
		{
			name: "fmt wraps standard error",
			err:  fmt.Errorf("cluster: %w", rootCause),
			root: rootCause,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeDubboCallError(tt.err)
			var timeoutErr net.Error
			if !stderrors.As(got, &timeoutErr) {
				t.Fatalf("normalizeDubboCallError(%v) did not return net.Error", tt.err)
			}
			if !timeoutErr.Timeout() {
				t.Fatalf("Timeout() = false for %v", got)
			}
			assert.Equal(t, tt.err.Error(), got.Error(), "original error message should be preserved")
			if tt.root != nil {
				assert.ErrorIs(t, got, tt.root, "original cause should remain discoverable")
			}
		})
	}
}

func TestNormalizeDubboCallErrorLeavesUnrelatedTimeoutTextAlone(t *testing.T) {
	err := stderrors.New("upstream timeout setting is invalid")
	got := normalizeDubboCallError(err)
	if got != err {
		t.Fatalf("normalizeDubboCallError(%v) changed unrelated error", err)
	}
	var timeoutErr net.Error
	if stderrors.As(got, &timeoutErr) {
		t.Fatalf("normalizeDubboCallError(%v) unexpectedly returned net.Error", err)
	}
}

func TestClose(t *testing.T) {
	client := SingletonDubboClient()
	client.GenericServicePool["key1"] = nil
	client.GenericServicePool["key2"] = nil
	client.GenericServicePool["key3"] = nil
	client.GenericServicePool["key4"] = nil
	assert.Equal(t, 4, len(client.GenericServicePool))
	client.Close()
	assert.Equal(t, 0, len(client.GenericServicePool))
}

func TestApply(t *testing.T) {
	dClient := NewDubboClient()
	err := dClient.Apply()
	assert.Nil(t, err)
}
