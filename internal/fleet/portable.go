package fleet

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Manual credentials have a separate, opt-in channel. Refuse credentials
// embedded in arbitrary headers/URLs rather than silently replicating them.
func portablePolicy(s Snapshot) error {
	s.Secrets = nil
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		return err
	}
	return checkPortable(value, "policy")
}
func secretName(name string) bool {
	name = strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
	switch name {
	case "apikey", "xapikey", "authorization", "cookie", "accesstoken", "refreshtoken", "idtoken", "clientsecret", "password", "token", "secret", "signature", "sig", "auth":
		return true
	}
	return false
}
func checkPortable(value any, path string) error {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if text, ok := child.(string); ok && text != "" && secretName(key) {
				return fmt.Errorf("credential-bearing %s.%s must stay device-local", path, key)
			}
			if err := checkPortable(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := checkPortable(child, path); err != nil {
				return err
			}
		}
	case string:
		if !strings.Contains(v, "://") {
			return nil
		}
		u, err := url.Parse(v)
		if err != nil {
			return nil
		}
		if u.User != nil {
			return fmt.Errorf("URL credentials in %s must stay device-local", path)
		}
		for key := range u.Query() {
			if secretName(key) || strings.EqualFold(key, "key") {
				return fmt.Errorf("URL credential parameter in %s must stay device-local", path)
			}
		}
	}
	return nil
}
