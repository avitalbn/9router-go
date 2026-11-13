// Package fleet implements revisioned, credential-preserving configuration
// replication and supervised deployment for independent local gateways.
package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const SchemaVersion = 1
const Repository = "avitalbn/9router-go"

type Object map[string]any
type Snapshot struct {
	Schema   int                                   `json:"schema"`
	Epoch    string                                `json:"epoch"`
	Revision uint64                                `json:"revision"`
	Hash     string                                `json:"hash"`
	Settings Object                                `json:"settings"`
	Tables   map[string][]Object                   `json:"tables"`
	KV       map[string]map[string]json.RawMessage `json:"kv"`
	Catalog  json.RawMessage                       `json:"catalog,omitempty"`
	Secrets  map[string]Object                     `json:"secrets,omitempty"`
}

var sharedSettings = words("providerStrategies comboStrategies providerThinking providerOverrides capacityAdapter forceFallback rtkEnabled cavemanEnabled cavemanLevel ponytailEnabled ponytailLevel adhdEnabled adhdLevel enableObservability observabilityMode headroomCodeAware claudeCacheTtl modelHealth")
var localSettings = words("password oidcClientSecret audit_chain_key autoUpdate requireApiKey headroomEnabled headroomUrl outboundProxyEnabled outboundProxyUrl maxRequestRecords retentionDays usageHistoryRetentionDays tailscaleEnabled tailscaleUrl tunnelDashboardAccess tunnelEnabled tunnelUrl navVisibility displayCurrency animationLevel requireLogin oidcEnabled oidcIssuer oidcClientId oidcAllowedEmails oidcRedirectUri")
var connectionPolicy = words("globalPriority defaultModel proxyPoolId prefix baseUrl inferenceBaseUrl nodeName apiType enabledModels connectionProxyEnabled connectionProxyUrl connectionNoProxy connectionProxyPoolId strictProxy vercelRelayUrl region mediaEnabled isMediaActive disabledKinds assignedModel allowPrivateNetworking")
var runtimeFields = words("accessToken refreshToken idToken apiKey expiresAt expiresIn lastRefreshAt tokenType scope projectId email displayName status testStatus lastError lastErrorAt lastErrorModel lastErrorSource errorCode backoffLevel consecutiveUseCount lastUsedAt rateLimitedUntil oauthFailureCount oauthLockedUntil accountBanFailureCount accountLockedUntil")
var localAccountFields = words("authMethod chatgptAccountId chatgptPlanType copilotToken copilotTokenExpiresAt email githubEmail githubLogin githubName githubUserId organizationId projectId systemId userId")
var nodeFields = words("prefix apiType baseUrl mediaEnabled isMediaActive disabledKinds")
var poolFields = words("name proxyUrl noProxy strictProxy type")
var poolRuntime = words("lastError lastTestedAt latency")
var sharedScopes = words("modelAliases customModels disabledModels pricing mitmAlias")
var tableColumns = map[string][]string{
	"combos":              words("id name kind models matchRules"),
	"providerNodes":       words("id type name rpmLimit tpmLimit concurrencyLimit tokenQuota queueTimeoutMs contractExpiresAt"),
	"providerConnections": words("id provider authType name priority isActive"),
	"proxyPools":          words("id isActive"),
}
var dataPolicy = map[string][]string{"providerConnections": connectionPolicy, "providerNodes": nodeFields, "proxyPools": poolFields}

func words(s string) []string { return strings.Fields(s) }
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func pick(src Object, keys []string) Object {
	out := Object{}
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}
func object(v any) Object {
	switch m := v.(type) {
	case map[string]any:
		return m
	case Object:
		return m
	}
	return Object{}
}
func decode(s string) (Object, error) {
	var m Object
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return m, nil
}
func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func (s Snapshot) PolicyHash() (string, error) {
	s.Epoch = ""
	s.Revision = 0
	s.Hash = ""
	s.Secrets = nil
	return digest(s)
}
func (s Snapshot) Validate() error {
	if s.Schema != SchemaVersion || s.Epoch == "" {
		return fmt.Errorf("unsupported fleet schema or missing authority epoch")
	}
	h, err := s.PolicyHash()
	if err != nil {
		return err
	}
	if h != s.Hash {
		return fmt.Errorf("configuration hash mismatch")
	}
	for table, rows := range s.Tables {
		cols, ok := tableColumns[table]
		if !ok {
			return fmt.Errorf("unknown managed table %s", table)
		}
		ids := map[string]bool{}
		for _, row := range rows {
			id, _ := row["id"].(string)
			if id == "" || ids[id] {
				return fmt.Errorf("missing or duplicate id in %s", table)
			}
			ids[id] = true
			for k := range row {
				if k != "data" && !contains(cols, k) {
					return fmt.Errorf("unknown %s column %s", table, k)
				}
			}
			if table == "combos" {
				var models []any
				if err := json.Unmarshal([]byte(fmt.Sprint(row["models"])), &models); err != nil {
					return fmt.Errorf("invalid combo model list")
				}
			}
		}
	}
	for table := range tableColumns {
		if _, ok := s.Tables[table]; !ok {
			return fmt.Errorf("incomplete snapshot: %s missing", table)
		}
	}
	for k := range s.Settings {
		if !contains(sharedSettings, k) {
			return fmt.Errorf("unknown managed setting %s", k)
		}
	}
	for k := range s.KV {
		if !contains(sharedScopes, k) {
			return fmt.Errorf("unknown managed scope %s", k)
		}
	}
	pools := map[string]bool{}
	for _, p := range s.Tables["proxyPools"] {
		pools[fmt.Sprint(p["id"])] = true
	}
	checkPool := func(m Object) error {
		for _, k := range []string{"proxyPoolId", "connectionProxyPoolId"} {
			if id, ok := m[k].(string); ok && id != "" && !pools[id] {
				return fmt.Errorf("missing referenced proxy pool")
			}
		}
		return nil
	}
	for _, c := range s.Tables["providerConnections"] {
		d := object(c["data"])
		if err := checkPool(d); err != nil {
			return err
		}
		if err := checkPool(object(d["providerSpecificData"])); err != nil {
			return err
		}
	}
	for _, v := range object(s.Settings["providerStrategies"]) {
		if err := checkPool(object(v)); err != nil {
			return err
		}
	}
	return nil
}

func policyData(table string, data Object) (Object, error) {
	allowed := dataPolicy[table]
	out := pick(data, allowed)
	for k := range data {
		if contains(allowed, k) || k == "providerSpecificData" {
			continue
		}
		if table == "providerConnections" && (contains(runtimeFields, k) || strings.HasPrefix(k, "modelLock_")) {
			continue
		}
		if table == "proxyPools" && contains(poolRuntime, k) {
			continue
		}
		return nil, fmt.Errorf("unclassified %s data field %s", table, k)
	}
	if table == "providerConnections" {
		nested := object(data["providerSpecificData"])
		policy := pick(nested, connectionPolicy)
		for k := range nested {
			if !contains(connectionPolicy, k) && !contains(localAccountFields, k) {
				return nil, fmt.Errorf("unclassified provider routing field %s", k)
			}
		}
		out["providerSpecificData"] = policy
	}
	for _, k := range []string{"proxyUrl", "connectionProxyUrl", "baseUrl"} {
		if raw, ok := out[k].(string); ok {
			u, err := url.Parse(raw)
			if err == nil && u.User != nil {
				u.User = nil
				out[k] = u.String()
			}
		}
	}
	return out, nil
}
func sortedTables() []string {
	keys := make([]string, 0, len(tableColumns))
	for k := range tableColumns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
