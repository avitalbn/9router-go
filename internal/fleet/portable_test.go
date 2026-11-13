package fleet

import "testing"

func TestPortablePolicyRejectsHiddenHeaderAndURLCredentials(t *testing.T) {
	path, _ := fixture(t, "manual-key")
	s := exportTest(t, path)
	s.Settings["providerOverrides"] = Object{"provider": Object{"headers": Object{"X-API-Key": "must-not-travel"}}}
	s.Hash, _ = s.PolicyHash()
	if s.Validate() == nil {
		t.Fatal("header credential entered shared policy")
	}
	delete(s.Settings, "providerOverrides")
	s.Tables["proxyPools"][0]["data"].(Object)["proxyUrl"] = "https://proxy.example.test/?token=must-not-travel"
	s.Hash, _ = s.PolicyHash()
	if s.Validate() == nil {
		t.Fatal("URL credential entered shared policy")
	}
}
