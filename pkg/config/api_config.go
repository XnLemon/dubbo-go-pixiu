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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

import (
	etcdv3 "github.com/dubbogo/gost/database/kv/etcd/v3"

	perrors "github.com/pkg/errors"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

import (
	"github.com/apache/dubbo-go-pixiu/pkg/common/yaml"
	"github.com/apache/dubbo-go-pixiu/pkg/logger"
	"github.com/apache/dubbo-go-pixiu/pkg/model"
)

const (
	DefaultTimeoutStr        = "1s"
	apiConfigWatchRetryDelay = 100 * time.Millisecond
)

var (
	apiConfig      *APIConfig
	listener       APIConfigResourceListener
	lock           sync.RWMutex
	listenerGateMu sync.Mutex
	listenerGate   chan struct{}
)

type apiConfigSource interface {
	Snapshot(prefix string) (keys, values []string, revision int64, err error)
	Watch(prefix string, revision int64) (clientv3.WatchChan, error)
	Done() <-chan struct{}
}

type etcdAPIConfigSource struct {
	client *etcdv3.Client
}

func (s *etcdAPIConfigSource) Snapshot(prefix string) ([]string, []string, int64, error) {
	raw := s.client.GetRawClient()
	if raw == nil {
		return nil, nil, 0, perrors.New("etcd raw client is nil")
	}
	response, err := raw.Get(s.client.GetCtx(), normalizeAPIConfigPrefix(prefix), clientv3.WithPrefix())
	if err != nil {
		return nil, nil, 0, err
	}
	keys := make([]string, 0, len(response.Kvs))
	values := make([]string, 0, len(response.Kvs))
	for _, item := range response.Kvs {
		keys = append(keys, string(item.Key))
		values = append(values, string(item.Value))
	}
	return keys, values, response.Header.Revision, nil
}

func (s *etcdAPIConfigSource) Watch(prefix string, revision int64) (clientv3.WatchChan, error) {
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	return s.client.WatchWithOption(normalizeAPIConfigPrefix(prefix), options...)
}

func (s *etcdAPIConfigSource) Done() <-chan struct{} {
	return s.client.Done()
}

func normalizeAPIConfigPrefix(prefix string) string {
	if strings.HasSuffix(prefix, "/") {
		return prefix
	}
	return prefix + "/"
}

func resetAPIConfigListenerGate() <-chan struct{} {
	listenerGateMu.Lock()
	defer listenerGateMu.Unlock()
	listenerGate = make(chan struct{})
	return listenerGate
}

var (
	BASE_INFO_NAME = "name"
	BASE_INFO_DESC = "description"
)

// APIConfigResourceListener defines api resource and method config listener interface
type APIConfigResourceListener interface {
	// ResourceChange handle modify resource event
	ResourceChange(new Resource, old Resource) bool // bool is return for interface implement is interesting
	// ResourceAdd handle add resource event
	ResourceAdd(res Resource) bool
	// ResourceDelete handle delete resource event
	ResourceDelete(deleted Resource) bool
	// MethodChange handle modify method event
	MethodChange(res Resource, method Method, old Method) bool
	// MethodAdd handle add method below one resource event
	MethodAdd(res Resource, method Method) bool
	// MethodDelete handle delete method event
	MethodDelete(res Resource, method Method) bool
}

// APIConfig defines the data structure of the api gateway configuration
type APIConfig struct {
	Name        string       `json:"name" yaml:"name"`
	Description string       `json:"description" yaml:"description"`
	Resources   []Resource   `json:"resources" yaml:"resources"`
	Definitions []Definition `json:"definitions" yaml:"definitions"`
}

// Resource defines the API path
type Resource struct {
	ID          int               `json:"id,omitempty" yaml:"id,omitempty"`
	Type        string            `json:"type" yaml:"type"` // Restful, Dubbo
	Path        string            `json:"path" yaml:"path"`
	Timeout     time.Duration     `json:"timeout" yaml:"timeout"`
	Description string            `json:"description" yaml:"description"`
	Filters     []Filter          `json:"filters" yaml:"filters"`
	Methods     []Method          `json:"methods" yaml:"methods"`
	Resources   []Resource        `json:"resources,omitempty" yaml:"resources,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// Filter filter with config
type Filter struct {
	Name   string         `json:"name,omitempty" yaml:"name,omitempty"`
	Config map[string]any `json:"config,omitempty" yaml:"config,omitempty" `
}

// Method defines the method of the api
type Method struct {
	ID                 int           `json:"id,omitempty" yaml:"id,omitempty"`
	ResourcePath       string        `json:"resourcePath" yaml:"resourcePath"`
	Enable             bool          `json:"enable" yaml:"enable"` // true means the method is up and false means method is down
	Timeout            time.Duration `json:"timeout" yaml:"timeout"`
	Mock               bool          `json:"mock" yaml:"mock"`
	Filters            []Filter      `json:"filters" yaml:"filters"`
	HTTPVerb           string        `json:"httpVerb" yaml:"httpVerb"`
	InboundRequest     `json:"inboundRequest" yaml:"inboundRequest"`
	IntegrationRequest `json:"integrationRequest" yaml:"integrationRequest"`
}

// InboundRequest defines the details of the inbound
type InboundRequest struct {
	RequestType  string           `json:"requestType" yaml:"requestType"` //http, TO-DO: dubbo
	Headers      []Params         `json:"headers" yaml:"headers"`
	QueryStrings []Params         `json:"queryStrings" yaml:"queryStrings"`
	RequestBody  []BodyDefinition `json:"requestBody" yaml:"requestBody"`
}

// Params defines the simple parameter definition
type Params struct {
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required" yaml:"required"`
}

// BodyDefinition connects the request body to the definitions
type BodyDefinition struct {
	DefinitionName string `json:"definitionName" yaml:"definitionName"`
}

// IntegrationRequest defines the backend request format and target
type IntegrationRequest struct {
	RequestType        string `json:"requestType" yaml:"requestType"` // dubbo, TO-DO: http
	DubboBackendConfig `json:"dubboBackendConfig,inline,omitempty" yaml:"dubboBackendConfig,inline,omitempty"`
	HTTPBackendConfig  `json:"httpBackendConfig,inline,omitempty" yaml:"httpBackendConfig,inline,omitempty"`
	MappingParams      []MappingParam `json:"mappingParams,omitempty" yaml:"mappingParams,omitempty"`
}

// MappingParam defines the mapping rules of headers and queryStrings
type MappingParam struct {
	Name    string `json:"name,omitempty" yaml:"name"`
	MapTo   string `json:"mapTo,omitempty" yaml:"mapTo"`
	MapType string `json:"mapType,omitempty" yaml:"mapType"`
}

// DubboBackendConfig defines the basic dubbo backend config
type DubboBackendConfig struct {
	ClusterName     string   `yaml:"clusterName" json:"clusterName"`
	ApplicationName string   `yaml:"applicationName" json:"applicationName"`
	Protocol        string   `yaml:"protocol" json:"protocol,omitempty" default:"dubbo"`
	Group           string   `yaml:"group" json:"group"`
	Version         string   `yaml:"version" json:"version"`
	Interface       string   `yaml:"interface" json:"interface"`
	Method          string   `yaml:"method" json:"method"`
	ParameterTypes  []string `yaml:"parameterTypes" json:"parameterTypes,omitempty"`
	Serialization   string   `yaml:"serialization" json:"serialization,omitempty"`
	Retries         string   `yaml:"retries" json:"retries,omitempty"`
}

// HTTPBackendConfig defines the basic dubbo backend config
type HTTPBackendConfig struct {
	URL string `yaml:"url" json:"url,omitempty"`
	// downstream host.
	Host string `yaml:"host" json:"host,omitempty"`
	// path to replace.
	Path string `yaml:"path" json:"path,omitempty"`
	// http protocol, http or https.
	Schema string `yaml:"schema" json:"scheme,omitempty"`
}

// Definition defines the complex json request body
type Definition struct {
	Name   string `json:"name" yaml:"name"`
	Schema string `json:"schema" yaml:"schema"` // use json schema
}

// Cluster defines the cluster config
type Cluster struct {
	Name    string `json:"name,omitempty" yaml:"name"`       // cluster name
	Type    string `json:"type,omitempty" yaml:"type"`       // cluster type
	Address string `json:"address,omitempty" yaml:"address"` // cluster address
	Port    int    `json:"port,omitempty" yaml:"port"`       // cluster port
	ID      int    `json:"id,omitempty" yaml:"id"`           // cluster id
}

// RouteConfig defines the route config
type RouteConfig struct {
	Routes []struct {
		Match struct {
			Prefix string `yaml:"prefix" json:"prefix"`
		} `yaml:"match" json:"match"`
		Route struct {
			Cluster                     string `yaml:"cluster" json:"cluster"`
			ClusterNotFoundResponseCode int    `yaml:"cluster_not_found_response_code" json:"cluster_not_found_response_code"`
		} `yaml:"route" json:"route"`
	} `yaml:"routes" json:"routes"`
}

// HTTPFilters defines the http filter
type HTTPFilters []struct {
	Name   string `yaml:"name" json:"name"`
	Config any    `yaml:"config" json:"config"`
}

// Listener defines the listener config
type Listener struct {
	Name    string `yaml:"name" json:"name"`
	Address struct {
		SocketAddress struct {
			Address string `yaml:"address" json:"address"`
			Port    int    `yaml:"port" json:"port"`
		} `yaml:"socket-address" json:"socket_address"`
		Name string `yaml:"name" json:"name"`
	} `yaml:"address" json:"address"`
	RouteConfig RouteConfig `yaml:"route_config" json:"route_config"`
	HTTPFilters HTTPFilters `yaml:"http_filters" json:"http_filters"`
}

// LoadAPIConfigFromFile load the api config from file
func LoadAPIConfigFromFile(path string) (*APIConfig, error) {
	if len(path) == 0 {
		return nil, perrors.Errorf("Config file not specified")
	}
	logger.Infof("Load API configuration file form %s", path)
	apiConf := &APIConfig{}
	err := yaml.UnmarshalYMLConfig(path, apiConf)
	if err != nil {
		return nil, perrors.Errorf("unmarshalYmlConfig error %s", perrors.WithStack(err))
	}
	apiConfig = apiConf
	return apiConf, nil
}

// LoadAPIConfig load the api config from config center
func LoadAPIConfig(metaConfig *model.APIMetaConfig) (*APIConfig, error) {
	tmpClient, err := etcdv3.NewConfigClientWithErr(
		etcdv3.WithName(etcdv3.RegistryETCDV3Client),
		etcdv3.WithTimeout(10*time.Second),
		etcdv3.WithEndpoints(strings.Split(metaConfig.Address, ",")...),
	)
	if err != nil {
		return nil, perrors.Errorf("Init etcd client fail error %s", err)
	}

	source := &etcdAPIConfigSource{client: tmpClient}
	loaded, watchRevision, err := loadAPIConfigFromSource(source, metaConfig.APIConfigPath)
	if err != nil {
		return nil, perrors.Errorf("Get remote config fail error %s", err)
	}
	// TODO: init other setting which need fetch from remote
	ready := resetAPIConfigListenerGate()
	go watchAPIConfigWithRecovery(source, metaConfig.APIConfigPath, watchRevision, ready)
	// TODO: watch other setting which need fetch from remote
	return loaded, nil
}

func loadAPIConfigFromSource(source apiConfigSource, path string) (*APIConfig, int64, error) {
	kList, vList, revision, err := source.Snapshot(path)
	if err != nil {
		return nil, 0, err
	}
	if err = initAPIConfigFromKVList(kList, vList); err != nil {
		return nil, 0, err
	}
	return apiConfig, revision + 1, nil
}

func watchAPIConfigWithRecovery(source apiConfigSource, path string, revision int64, ready <-chan struct{}) {
	for {
		listenResourceAndMethodEvent(source, path, revision, ready)
		if apiConfigSourceStopped(source) {
			return
		}
		if !waitForAPIConfigListener(source, ready) {
			return
		}
		ready = nil

		previousResources := apiConfigResourcesSnapshot()
		_, nextRevision, err := loadAPIConfigFromSource(source, path)
		if err != nil {
			logger.Warnf("reload api config after watch interruption {key:%s} = error{%s}", path, err)
			if !waitForAPIConfigWatchRetry(source) {
				return
			}
			continue
		}
		reconcileAPIConfigResources(previousResources, apiConfigResourcesSnapshot())
		revision = nextRevision
	}
}

func waitForAPIConfigListener(source apiConfigSource, ready <-chan struct{}) bool {
	if ready == nil {
		return true
	}
	select {
	case <-source.Done():
		logger.Warnf("client stopped before api config listener became ready")
		return false
	case <-ready:
		return true
	}
}

func apiConfigSourceStopped(source apiConfigSource) bool {
	select {
	case <-source.Done():
		return true
	default:
		return false
	}
}

func waitForAPIConfigWatchRetry(source apiConfigSource) bool {
	timer := time.NewTimer(apiConfigWatchRetryDelay)
	defer timer.Stop()
	select {
	case <-source.Done():
		return false
	case <-timer.C:
		return true
	}
}

func apiConfigResourcesSnapshot() []Resource {
	lock.RLock()
	defer lock.RUnlock()
	if apiConfig == nil || len(apiConfig.Resources) == 0 {
		return nil
	}
	resources := make([]Resource, len(apiConfig.Resources))
	copy(resources, apiConfig.Resources)
	return resources
}

func reconcileAPIConfigResources(previousResources, currentResources []Resource) {
	listenerGateMu.Lock()
	currentListener := listener
	listenerGateMu.Unlock()
	if currentListener == nil {
		logger.Warnf("skip api config runtime reconciliation because no listener is registered")
		return
	}

	// Rebuild from the fresh snapshot so a canceled watch cannot leave stale
	// routes in the runtime, even when it missed several resource mutations.
	for _, resource := range previousResources {
		if !currentListener.ResourceDelete(resource) {
			logger.Warnf("remove stale api resource %q during watch recovery failed", resource.Path)
		}
	}
	for _, resource := range currentResources {
		if !currentListener.ResourceAdd(resource) {
			logger.Warnf("add api resource %q during watch recovery failed", resource.Path)
		}
	}
}

func initAPIConfigFromKVList(kList, vList []string) error {
	var skList, svList, mkList, mvList []string
	var baseInfo string

	for i, k := range kList {
		v := vList[i]
		//handle base info
		re := getCheckBaseInfoRegexp()
		if m := re.Match([]byte(k)); m {
			baseInfo = v
			continue
		}

		// handle resource
		re = getCheckResourceRegexp()
		if m := re.Match([]byte(k)); m {
			skList = append(skList, k)
			svList = append(svList, v)
			continue
		}
		// handle method
		re = getExtractMethodRegexp()
		if m := re.Match([]byte(k)); m {
			mkList = append(mkList, k)
			mvList = append(mvList, v)
			continue
		}
	}

	lock.Lock()
	defer lock.Unlock()

	tmpApiConf := &APIConfig{}
	if err := initBaseInfoFromString(tmpApiConf, baseInfo); err != nil {
		logger.Errorf("initBaseInfoFromString error %s", err)
		return err
	}
	if err := initAPIConfigServiceFromKvList(tmpApiConf, skList, svList); err != nil {
		logger.Errorf("initAPIConfigServiceFromKvList error %s", err)
		return err
	}
	if err := initAPIConfigMethodFromKvList(tmpApiConf, mkList, mvList); err != nil {
		logger.Errorf("initAPIConfigMethodFromKvList error %s", err)
		return err
	}

	apiConfig = tmpApiConf
	return nil
}

func initBaseInfoFromString(conf *APIConfig, str string) error {
	properties := make(map[string]string, 8)
	if err := yaml.UnmarshalYML([]byte(str), properties); err != nil {
		logger.Errorf("unmarshalYmlConfig error %s", err)
		return err
	}
	if v, ok := properties[BASE_INFO_NAME]; ok {
		conf.Name = v
	}
	if v, ok := properties[BASE_INFO_DESC]; ok {
		conf.Description = v
	}
	return nil
}

func initAPIConfigMethodFromKvList(config *APIConfig, kList, vList []string) error {
	for i := range kList {
		v := vList[i]
		method := &Method{}
		err := yaml.UnmarshalYML([]byte(v), method)
		if err != nil {
			logger.Errorf("unmarshalYmlConfig error %s", err)
			return err
		}

		found := false
		for r, resource := range config.Resources {
			if method.ResourcePath != resource.Path {
				continue
			}

			for j, old := range resource.Methods {
				if old.HTTPVerb == method.HTTPVerb {
					// modify one method
					resource.Methods[j] = *method
					found = true
				}
			}
			if !found {
				resource.Methods = append(resource.Methods, *method)
				config.Resources[r] = resource
				found = true
			}
		}

		// not found one resource, so need add empty resource first
		if !found {
			resource := &Resource{}
			resource.Methods = append(resource.Methods, *method)
			resource.Path = method.ResourcePath
			config.Resources = append(config.Resources, *resource)
		}
	}
	return nil
}

func initAPIConfigServiceFromKvList(config *APIConfig, kList, vList []string) error {
	for i := range kList {
		v := vList[i]
		resource := &Resource{}
		err := yaml.UnmarshalYML([]byte(v), resource)
		if err != nil {
			logger.Errorf("unmarshalYmlConfig error %s", err)
			return err
		}

		found := false
		if config.Resources == nil {
			config.Resources = make([]Resource, 0)
		}

		for i, old := range config.Resources {
			if old.Path != resource.Path {
				continue
			}
			// replace old with new one except method list
			resource.Methods = old.Methods
			config.Resources[i] = *resource
			found = true
		}

		if !found {
			config.Resources = append(config.Resources, *resource)
		}
		continue
	}
	return nil
}

func listenResourceAndMethodEvent(source apiConfigSource, key string, revision int64, ready <-chan struct{}) bool {
	wc, err := source.Watch(key, revision)
	if err != nil {
		logger.Warnf("Watch api config {key:%s, revision:%d} = error{%s}", key, revision, err)
		return false
	}
	if !waitForAPIConfigListener(source, ready) {
		return false
	}
	for {
		select {
		// client stopped
		case <-source.Done():
			logger.Warnf("client stopped")
			return false
		case e, ok := <-wc:
			if !ok {
				logger.Warnf("watch-chan closed")
				return false
			}

			if e.Canceled {
				logger.Warnf("watch canceled {compact-revision:%d}", e.CompactRevision)
				return false
			}
			if e.Err() != nil {
				logger.Errorf("watch ERR {err: %s}", e.Err())
				return false
			}
			for _, event := range e.Events {
				switch event.Type {
				case mvccpb.PUT:
					logger.Infof("get event (key{%s}) = event{EventNodePut}", event.Kv.Key)
					handlePutEvent(event.Kv.Key, event.Kv.Value)
				case mvccpb.DELETE:
					logger.Infof("get event (key{%s}) = event{EventNodeDeleted}", event.Kv.Key)
					handleDeleteEvent(event.Kv.Key, event.Kv.Value)
				default:
					logger.Infof("get event (key{%s}) = event{%d}", event.Kv.Key, event.Type)
				}
			}
		}
	}
}

func handleDeleteEvent(key, _ []byte) {
	lock.Lock()
	defer lock.Unlock()

	keyStr := string(key)
	keyStr = strings.TrimSuffix(keyStr, "/")

	re := getCheckResourceRegexp()
	if m := re.Match(key); m {
		pathArray := strings.Split(keyStr, "/")
		if len(pathArray) == 0 {
			logger.Errorf("handleDeleteEvent key format error")
			return
		}
		resourceIdStr := pathArray[len(pathArray)-1]
		ID, err := strconv.Atoi(resourceIdStr)
		if err != nil {
			logger.Errorf("handleDeleteEvent ID is not int error %s", err)
			return
		}
		deleteApiConfigResource(ID)
		return
	}

	re = getExtractMethodRegexp()
	if m := re.Match(key); m {
		pathArray := strings.Split(keyStr, "/")
		if len(pathArray) < 3 {
			logger.Errorf("handleDeleteEvent key format error")
			return
		}
		resourceIdStr := pathArray[len(pathArray)-3]
		resourceId, err := strconv.Atoi(resourceIdStr)
		if err != nil {
			logger.Errorf("handleDeleteEvent ID is not int error %s", err)
			return
		}

		methodIdStr := pathArray[len(pathArray)-1]
		methodId, err := strconv.Atoi(methodIdStr)
		if err != nil {
			logger.Errorf("handleDeleteEvent ID is not int error %s", err)
			return
		}
		deleteApiConfigMethod(resourceId, methodId)
	}
}

func handlePutEvent(key, val []byte) {
	lock.Lock()
	defer lock.Unlock()

	re := getCheckResourceRegexp()
	if m := re.Match(key); m {
		res := &Resource{}
		err := yaml.UnmarshalYML(val, res)
		if err != nil {
			logger.Errorf("handlePutEvent UnmarshalYML error %s", err)
			return
		}
		mergeApiConfigResource(*res)
		return
	}

	re = getExtractMethodRegexp()
	if m := re.Match(key); m {
		res := &Method{}
		err := yaml.UnmarshalYML(val, res)
		if err != nil {
			logger.Errorf("handlePutEvent UnmarshalYML error %s", err)
			return
		}
		mergeApiConfigMethod(res.ResourcePath, *res)
		return
	}

	//handle base info
	re = getCheckBaseInfoRegexp()
	if m := re.Match(key); m {
		mergeBaseInfo(val)
		return
	}
}

func deleteApiConfigResource(resourceId int) {
	for i := 0; i < len(apiConfig.Resources); i++ {
		itr := apiConfig.Resources[i]
		if itr.ID == resourceId {
			apiConfig.Resources = append(apiConfig.Resources[:i], apiConfig.Resources[i+1:]...)
			listener.ResourceDelete(itr)
			return
		}
	}
}

func mergeApiConfigResource(val Resource) {
	for i, resource := range apiConfig.Resources {
		if val.ID != resource.ID {
			continue
		}
		// modify one resource
		val.Methods = resource.Methods
		apiConfig.Resources[i] = val
		listener.ResourceChange(val, resource)
		return
	}
	// add one resource
	apiConfig.Resources = append(apiConfig.Resources, val)
	listener.ResourceAdd(val)
}

func mergeBaseInfo(val []byte) {
	_ = initBaseInfoFromString(apiConfig, string(val))
}

func deleteApiConfigMethod(resourceId, methodId int) {
	for _, resource := range apiConfig.Resources {
		if resource.ID != resourceId {
			continue
		}

		for i := 0; i < len(resource.Methods); i++ {
			method := resource.Methods[i]

			if method.ID == methodId {
				resource.Methods = append(resource.Methods[:i], resource.Methods[i+1:]...)
				listener.MethodDelete(resource, method)
				return
			}
		}
	}
}

func mergeApiConfigMethod(path string, val Method) {
	for i, resource := range apiConfig.Resources {
		if path != resource.Path {
			continue
		}

		for j, method := range resource.Methods {
			if method.ID == val.ID {
				// modify one method
				resource.Methods[j] = val
				listener.MethodChange(resource, val, method)
				apiConfig.Resources[i] = resource
				return
			}
		}
		// add one method
		resource.Methods = append(resource.Methods, val)
		apiConfig.Resources[i] = resource
		listener.MethodAdd(resource, val)
	}
}

func getCheckBaseInfoRegexp() *regexp.Regexp {
	return regexp.MustCompile(".+/base$")
}

func getCheckResourceRegexp() *regexp.Regexp {
	return regexp.MustCompile(".+/resources/[^/]+/?$")
}

func getExtractMethodRegexp() *regexp.Regexp {
	return regexp.MustCompile(".+/resources/([^/]+)/method/[^/]+/?$")
}

// nolint
func getCheckRatelimitRegexp() *regexp.Regexp {
	return regexp.MustCompile(".+/filter/ratelimit")
}

// RegisterConfigListener register APIConfigListener
func RegisterConfigListener(li APIConfigResourceListener) {
	listenerGateMu.Lock()
	listener = li
	if listenerGate != nil {
		close(listenerGate)
		listenerGate = nil
	}
	listenerGateMu.Unlock()
}

// UnmarshalYAML Resource custom UnmarshalYAML
func (r *Resource) UnmarshalYAML(unmarshal func(any) error) error {
	s := &struct {
		Timeout string `yaml:"timeout"`
	}{}
	type Alias Resource
	alias := (*Alias)(r)
	if err := unmarshal(alias); err != nil {
		return err
	}
	if err := unmarshal(s); err != nil {
		return err
	}
	// if timeout is empty must set a default value. if "" used to time.ParseDuration will err.
	if s.Timeout == "" {
		s.Timeout = DefaultTimeoutStr
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return err
	}

	r.Timeout = d

	return nil
}

// UnmarshalYAML method custom UnmarshalYAML
func (m *Method) UnmarshalYAML(unmarshal func(any) error) error {
	type Alias Method
	alias := (*Alias)(m)
	if err := unmarshal(alias); err != nil {
		return err
	}
	s := &struct {
		Timeout string `yaml:"timeout"`
	}{}
	if err := unmarshal(s); err != nil {
		return err
	}
	// if timeout is empty must set a default value. if "" used to time.ParseDuration will err.
	if s.Timeout == "" {
		s.Timeout = DefaultTimeoutStr
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil {
		return err
	}
	m.Timeout = d
	return nil
}
