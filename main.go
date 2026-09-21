package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginID = "antigravity-request-rewrite"

var pluginVersion = "0.1.0-dev"

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
}

type rawConfig struct {
	TargetFormats []string  `yaml:"target_formats"`
	Rules         []rawRule `yaml:"rules"`
}

type rawRule struct {
	Name          string   `yaml:"name"`
	Enabled       *bool    `yaml:"enabled"`
	Mode          string   `yaml:"mode"`
	Pattern       string   `yaml:"pattern"`
	Replacement   string   `yaml:"replacement"`
	SourceFormats []string `yaml:"source_formats"`
	Scopes        []string `yaml:"scopes"`
	Models        []string `yaml:"models"`
}

type compiledConfig struct {
	TargetFormats map[string]struct{}
	Rules         []compiledRule
}

type compiledRule struct {
	Name          string
	Mode          string
	Pattern       string
	Replacement   string
	Regex         *regexp.Regexp
	SourceFormats map[string]struct{}
	Scopes        map[string]struct{}
	ModelPatterns []*regexp.Regexp
}

var configState = struct {
	sync.RWMutex
	cfg compiledConfig
}{cfg: compiledConfig{}}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		return interceptAfterAuth(request)
	case pluginabi.MethodRequestComplete:
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Antigravity Request Rewrite",
			Version:          pluginVersion,
			Author:           "cuteyuchen",
			GitHubRepository: "https://github.com/cuteyuchen/cpa-plugin-antigravity-request-rewrite",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "target_formats",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Target upstream formats to rewrite. Defaults to antigravity. grok/x-ai aliases normalize to xai.",
				},
				{
					Name:        "rules",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Ordered request rewrite rules. Supports replace and regex_replace modes.",
				},
			},
		},
		Capabilities: registrationCapabilities{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
		},
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("decode lifecycle request: %w", err)
		}
	}
	if req.SchemaVersion != 0 && req.SchemaVersion < 2 {
		return fmt.Errorf("request interceptor requires host schema version 2 or newer")
	}

	next, err := compileConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	configState.Lock()
	configState.cfg = next
	configState.Unlock()
	return nil
}

func compileConfig(raw []byte) (compiledConfig, error) {
	var cfg rawConfig
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return compiledConfig{}, fmt.Errorf("decode plugin config: %w", err)
		}
	}

	targetFormats := normalizeTargetFormatSet(cfg.TargetFormats)
	if len(targetFormats) == 0 {
		targetFormats = map[string]struct{}{"antigravity": {}}
	}

	out := compiledConfig{
		TargetFormats: targetFormats,
		Rules:         make([]compiledRule, 0, len(cfg.Rules)),
	}
	for i, rule := range cfg.Rules {
		if rule.Enabled != nil && !*rule.Enabled {
			continue
		}
		compiled, err := compileRule(rule, i)
		if err != nil {
			return compiledConfig{}, err
		}
		out.Rules = append(out.Rules, compiled)
	}
	return out, nil
}

func compileRule(rule rawRule, index int) (compiledRule, error) {
	name := strings.TrimSpace(rule.Name)
	if name == "" {
		name = fmt.Sprintf("rule-%d", index+1)
	}

	mode := strings.ToLower(strings.TrimSpace(rule.Mode))
	switch mode {
	case "replace", "regex_replace":
	default:
		return compiledRule{}, fmt.Errorf("%s: mode must be replace or regex_replace", name)
	}

	pattern := rule.Pattern
	if pattern == "" {
		return compiledRule{}, fmt.Errorf("%s: pattern must not be empty", name)
	}

	compiled := compiledRule{
		Name:          name,
		Mode:          mode,
		Pattern:       pattern,
		Replacement:   rule.Replacement,
		SourceFormats: normalizeSet(rule.SourceFormats),
		Scopes:        normalizeSet(rule.Scopes),
	}
	if len(compiled.Scopes) == 0 {
		compiled.Scopes = map[string]struct{}{"system": {}, "developer": {}}
	}
	for scope := range compiled.Scopes {
		if scope != "system" && scope != "developer" {
			return compiledRule{}, fmt.Errorf("%s: unsupported scope %q", name, scope)
		}
	}

	if mode == "regex_replace" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return compiledRule{}, fmt.Errorf("%s: invalid regex: %w", name, err)
		}
		compiled.Regex = re
	}

	for _, modelPattern := range rule.Models {
		modelPattern = strings.TrimSpace(modelPattern)
		if modelPattern == "" {
			continue
		}
		re, err := compileGlob(modelPattern)
		if err != nil {
			return compiledRule{}, fmt.Errorf("%s: invalid model pattern %q: %w", name, modelPattern, err)
		}
		compiled.ModelPatterns = append(compiled.ModelPatterns, re)
	}
	return compiled, nil
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte('$')
	return regexp.Compile(b.String())
}

func normalizeSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeFormat(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func normalizeFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "responses", "openai_responses", "openai-responses":
		return "openai-response"
	case "chat", "chat-completions", "openai-chat":
		return "openai"
	default:
		return value
	}
}

func normalizeTargetFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "anti-gravity":
		return "antigravity"
	case "grok", "x-ai", "x.ai":
		return "xai"
	default:
		return value
	}
}

func normalizeTargetFormatSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeTargetFormat(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func targetFormatEnabled(cfg compiledConfig, toFormat string) bool {
	normalized := normalizeTargetFormat(toFormat)
	if _, ok := cfg.TargetFormats["*"]; ok {
		return true
	}
	_, ok := cfg.TargetFormats[normalized]
	return ok
}

func currentConfig() compiledConfig {
	configState.RLock()
	defer configState.RUnlock()

	out := compiledConfig{
		TargetFormats: configState.cfg.TargetFormats,
		Rules:         make([]compiledRule, len(configState.cfg.Rules)),
	}
	copy(out.Rules, configState.cfg.Rules)
	return out
}

func interceptAfterAuth(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode request.intercept_after request: %w", err)
	}

	cfg := currentConfig()
	if !targetFormatEnabled(cfg, req.ToFormat) {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	if len(cfg.Rules) == 0 || len(req.Body) == 0 {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}

	body, changed, err := rewriteBody(req.Body, req.SourceFormat, req.Model, cfg)
	if err != nil {
		// Fail open: malformed/unsupported client payloads should not take the proxy down.
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	if !changed {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}

	return okEnvelope(pluginapi.RequestInterceptResponse{
		Body:         body,
		ClearHeaders: []string{"Content-Encoding", "Content-Length", "Transfer-Encoding"},
	})
}

func rewriteBody(body []byte, sourceFormat, model string, cfg compiledConfig) ([]byte, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, false, err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("request body must be a JSON object")
	}

	changed := false
	applyText := func(scope, text string) (string, bool) {
		next := text
		localChanged := false
		for _, rule := range cfg.Rules {
			if !ruleApplies(rule, sourceFormat, model, scope) {
				continue
			}
			var after string
			switch rule.Mode {
			case "replace":
				after = strings.ReplaceAll(next, rule.Pattern, rule.Replacement)
			case "regex_replace":
				after = rule.Regex.ReplaceAllString(next, rule.Replacement)
			default:
				after = next
			}
			if after != next {
				next = after
				localChanged = true
			}
		}
		return next, localChanged
	}

	if value, exists := obj["instructions"]; exists {
		if next, okChanged := rewriteStringValue(value, "system", applyText); okChanged {
			obj["instructions"] = next
			changed = true
		}
	}

	if rewriteRoleMessages(obj["input"], applyText) {
		changed = true
	}
	if rewriteRoleMessages(obj["messages"], applyText) {
		changed = true
	}

	if value, exists := obj["system"]; exists {
		if next, okChanged := rewriteTextContainer(value, "system", applyText); okChanged {
			obj["system"] = next
			changed = true
		}
	}

	for _, key := range []string{"systemInstruction", "system_instruction"} {
		if value, exists := obj[key]; exists {
			if next, okChanged := rewriteGeminiSystem(value, applyText); okChanged {
				obj[key] = next
				changed = true
			}
		}
	}

	if wrapped, ok := obj["request"].(map[string]any); ok {
		for _, key := range []string{"systemInstruction", "system_instruction"} {
			if value, exists := wrapped[key]; exists {
				if next, okChanged := rewriteGeminiSystem(value, applyText); okChanged {
					wrapped[key] = next
					changed = true
				}
			}
		}
	}

	if !changed {
		return nil, false, nil
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func ruleApplies(rule compiledRule, sourceFormat, model, scope string) bool {
	if _, ok := rule.Scopes[strings.ToLower(scope)]; !ok {
		return false
	}
	if len(rule.SourceFormats) != 0 {
		if _, ok := rule.SourceFormats[normalizeFormat(sourceFormat)]; !ok {
			return false
		}
	}
	if len(rule.ModelPatterns) != 0 {
		matched := false
		for _, pattern := range rule.ModelPatterns {
			if pattern.MatchString(model) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func rewriteStringValue(value any, scope string, apply func(string, string) (string, bool)) (any, bool) {
	text, ok := value.(string)
	if !ok {
		return value, false
	}
	next, changed := apply(scope, text)
	return next, changed
}

func rewriteRoleMessages(value any, apply func(string, string) (string, bool)) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "system" && role != "developer" {
			continue
		}
		content, exists := msg["content"]
		if !exists {
			continue
		}
		next, itemChanged := rewriteTextContainer(content, role, apply)
		if itemChanged {
			msg["content"] = next
			changed = true
		}
	}
	return changed
}

func rewriteTextContainer(value any, scope string, apply func(string, string) (string, bool)) (any, bool) {
	switch typed := value.(type) {
	case string:
		return rewriteStringValue(typed, scope, apply)
	case []any:
		changed := false
		for i, item := range typed {
			switch block := item.(type) {
			case string:
				next, itemChanged := apply(scope, block)
				if itemChanged {
					typed[i] = next
					changed = true
				}
			case map[string]any:
				blockType, _ := block["type"].(string)
				blockType = strings.ToLower(strings.TrimSpace(blockType))
				if blockType != "" && blockType != "text" && blockType != "input_text" {
					continue
				}
				if text, ok := block["text"].(string); ok {
					next, itemChanged := apply(scope, text)
					if itemChanged {
						block["text"] = next
						changed = true
					}
				}
			}
		}
		return typed, changed
	default:
		return value, false
	}
}

func rewriteGeminiSystem(value any, apply func(string, string) (string, bool)) (any, bool) {
	obj, ok := value.(map[string]any)
	if !ok {
		return value, false
	}
	parts, ok := obj["parts"].([]any)
	if !ok {
		return value, false
	}
	changed := false
	for _, part := range parts {
		block, ok := part.(map[string]any)
		if !ok {
			continue
		}
		text, ok := block["text"].(string)
		if !ok {
			continue
		}
		next, itemChanged := apply("system", text)
		if itemChanged {
			block["text"] = next
			changed = true
		}
	}
	return obj, changed
}

type successEnvelope struct {
	OK     bool `json:"ok"`
	Result any  `json:"result"`
}

func okEnvelope(value any) ([]byte, error) {
	return json.Marshal(successEnvelope{OK: true, Result: value})
}

func errorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(pluginabi.Envelope{
		OK:    false,
		Error: &pluginabi.Error{Code: code, Message: message},
	})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"failed to encode error envelope"}}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

var _ = http.MethodPost
