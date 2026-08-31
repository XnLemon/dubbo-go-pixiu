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
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

import (
	adminconfig "github.com/apache/dubbo-go-pixiu/admin/config"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// RouteBindingKV is the small storage contract needed by the route-binding
// lifecycle. Keeping it independent from the etcd client makes the lifecycle
// testable without requiring an external etcd process.
type RouteBindingKV struct {
	Key   string
	Value string
}

// RouteBindingStore stores control-plane records and applies a publish plan.
// Commit is the atomic boundary for a published snapshot: the canonical
// record, history entry, and generated legacy keys are written together.
type RouteBindingStore interface {
	Get(key string) (value string, exists bool, err error)
	List(prefix string) ([]RouteBindingKV, error)
	Put(key, value string) error
	Delete(key string) error
	NextID(counterKey string) (int, error)
	Commit(puts []RouteBindingKV, deletes []string) error
}

type etcdRouteBindingStore struct{}

// newEtcdRouteBindingStore binds the lifecycle to the Admin's already opened
// etcd client. The client is intentionally not opened a second time.
func newEtcdRouteBindingStore() (RouteBindingStore, error) {
	if adminconfig.Client == nil {
		return nil, fmt.Errorf("admin etcd client is not initialized")
	}
	if adminconfig.Client.GetRawClient() == nil {
		return nil, fmt.Errorf("admin etcd raw client is not initialized")
	}
	return &etcdRouteBindingStore{}, nil
}

func (s *etcdRouteBindingStore) rawClient() (*clientv3.Client, context.Context, error) {
	if adminconfig.Client == nil {
		return nil, nil, fmt.Errorf("admin etcd client is not initialized")
	}
	raw := adminconfig.Client.GetRawClient()
	if raw == nil {
		return nil, nil, fmt.Errorf("admin etcd raw client is not initialized")
	}
	ctx := adminconfig.Client.GetCtx()
	if ctx == nil {
		ctx = context.Background()
	}
	return raw, ctx, nil
}

func (s *etcdRouteBindingStore) Get(key string) (string, bool, error) {
	raw, ctx, err := s.rawClient()
	if err != nil {
		return "", false, err
	}
	response, err := raw.Get(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("get route binding key %q: %w", key, err)
	}
	if len(response.Kvs) == 0 {
		return "", false, nil
	}
	return string(response.Kvs[0].Value), true, nil
}

func (s *etcdRouteBindingStore) List(prefix string) ([]RouteBindingKV, error) {
	if strings.TrimSpace(prefix) == "" {
		return nil, fmt.Errorf("route binding list prefix is empty")
	}
	raw, ctx, err := s.rawClient()
	if err != nil {
		return nil, err
	}
	response, err := raw.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("list route binding keys under %q: %w", prefix, err)
	}
	items := make([]RouteBindingKV, 0, len(response.Kvs))
	for _, item := range response.Kvs {
		items = append(items, RouteBindingKV{Key: string(item.Key), Value: string(item.Value)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *etcdRouteBindingStore) Put(key, value string) error {
	raw, ctx, err := s.rawClient()
	if err != nil {
		return err
	}
	if _, err = raw.Put(ctx, key, value); err != nil {
		return fmt.Errorf("put route binding key %q: %w", key, err)
	}
	return nil
}

func (s *etcdRouteBindingStore) Delete(key string) error {
	raw, ctx, err := s.rawClient()
	if err != nil {
		return err
	}
	if _, err = raw.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete route binding key %q: %w", key, err)
	}
	return nil
}

func (s *etcdRouteBindingStore) NextID(counterKey string) (int, error) {
	raw, ctx, err := s.rawClient()
	if err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 100; attempt++ {
		response, getErr := raw.Get(ctx, counterKey)
		if getErr != nil {
			return 0, fmt.Errorf("get route binding id counter %q: %w", counterKey, getErr)
		}

		current := 0
		var compare clientv3.Cmp
		if len(response.Kvs) == 0 {
			compare = clientv3.Compare(clientv3.CreateRevision(counterKey), "=", 0)
		} else {
			current, err = strconv.Atoi(string(response.Kvs[0].Value))
			if err != nil || current < 0 {
				return 0, fmt.Errorf("route binding id counter %q is invalid", counterKey)
			}
			compare = clientv3.Compare(clientv3.ModRevision(counterKey), "=", response.Kvs[0].ModRevision)
		}

		next := current + 1
		transaction, txnErr := raw.Txn(ctx).
			If(compare).
			Then(clientv3.OpPut(counterKey, strconv.Itoa(next))).
			Commit()
		if txnErr != nil {
			return 0, fmt.Errorf("increment route binding id counter %q: %w", counterKey, txnErr)
		}
		if transaction.Succeeded {
			return next, nil
		}
	}
	return 0, fmt.Errorf("increment route binding id counter %q: concurrent updates did not settle", counterKey)
}

func (s *etcdRouteBindingStore) Commit(puts []RouteBindingKV, deletes []string) error {
	if len(puts) == 0 && len(deletes) == 0 {
		return nil
	}
	raw, ctx, err := s.rawClient()
	if err != nil {
		return err
	}
	ops := make([]clientv3.Op, 0, len(puts)+len(deletes))
	for _, put := range puts {
		if strings.TrimSpace(put.Key) == "" {
			return fmt.Errorf("route binding publish key is empty")
		}
		ops = append(ops, clientv3.OpPut(put.Key, put.Value))
	}
	for _, key := range deletes {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("route binding delete key is empty")
		}
		ops = append(ops, clientv3.OpDelete(key))
	}
	response, err := raw.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return fmt.Errorf("commit route binding publish plan: %w", err)
	}
	if !response.Succeeded {
		return fmt.Errorf("commit route binding publish plan was not committed")
	}
	return nil
}
