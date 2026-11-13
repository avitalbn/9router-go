package fleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, key string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db", "data.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	db, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	statements := []string{
		"PRAGMA journal_mode=WAL",
		"CREATE TABLE settings(id INTEGER PRIMARY KEY,data TEXT)",
		"CREATE TABLE kv(scope TEXT,key TEXT,value TEXT,PRIMARY KEY(scope,key))",
		"CREATE TABLE combos(id TEXT PRIMARY KEY,name TEXT UNIQUE,kind TEXT,models TEXT,matchRules TEXT,createdAt TEXT,updatedAt TEXT)",
		"CREATE TABLE providerNodes(id TEXT PRIMARY KEY,type TEXT,name TEXT,rpmLimit INTEGER,tpmLimit INTEGER,concurrencyLimit INTEGER,tokenQuota INTEGER,queueTimeoutMs INTEGER,contractExpiresAt TEXT,data TEXT,createdAt TEXT,updatedAt TEXT)",
		"CREATE TABLE proxyPools(id TEXT PRIMARY KEY,isActive INTEGER,testStatus TEXT,data TEXT,createdAt TEXT,updatedAt TEXT)",
		"CREATE TABLE providerConnections(id TEXT PRIMARY KEY,provider TEXT,authType TEXT,name TEXT,priority INTEGER,isActive INTEGER,email TEXT,data TEXT,createdAt TEXT,updatedAt TEXT,lastUsedAt TEXT,secretCiphertext TEXT,secretWrappedDEK TEXT,tokenCiphertext TEXT,tokenWrappedDEK TEXT,refreshCiphertext TEXT,refreshWrappedDEK TEXT)",
		"CREATE TABLE apiKeys(id TEXT PRIMARY KEY,key TEXT,isActive INTEGER DEFAULT 1,keyHash TEXT DEFAULT '')",
		"CREATE TABLE usageHistory(id INTEGER PRIMARY KEY,promptTokens INTEGER)",
		"INSERT INTO settings VALUES(1,'{\"providerThinking\":{\"p\":\"high\"},\"password\":\"local-password\",\"requireApiKey\":true}')",
		"INSERT INTO combos VALUES('chain','coding','llm','[\"p/a\",\"p/b\"]','[]','old','old')",
		"INSERT INTO providerNodes VALUES('node','openai-compatible','node',10,100,2,0,0,'','{\"prefix\":\"p\",\"baseUrl\":\"https://example.test/v1\",\"apiType\":\"chat\"}','old','old')",
		"INSERT INTO proxyPools VALUES('pool',1,'ok','{\"name\":\"pool\",\"proxyUrl\":\"http://proxy.test:8080\",\"strictProxy\":true,\"lastError\":\"local-health\"}','old','old')",
		"INSERT INTO kv VALUES('disabledModels','p','[\"hidden\"]')",
		"INSERT INTO apiKeys(id,key) VALUES('client','local-client-key')",
		"INSERT INTO usageHistory VALUES(1,12345)",
	}
	for _, q := range statements {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	data := Object{"apiKey": key, "refreshToken": "local-refresh-" + key, "accessToken": "local-access-" + key, "testStatus": "local", "defaultModel": "a", "providerSpecificData": Object{"enabledModels": []string{"a", "b"}, "chatgptAccountId": "local-account-" + key, "connectionProxyPoolId": "pool"}}
	b, _ := json.Marshal(data)
	if _, err = db.Exec("INSERT INTO providerConnections(id,provider,authType,name,priority,isActive,data,createdAt,updatedAt,secretCiphertext) VALUES('account','p','apikey','account',1,1,?,'old','old','')", string(b)); err != nil {
		t.Fatal(err)
	}
	return path, db
}
func exportTest(t *testing.T, path string) Snapshot {
	t.Helper()
	s, err := Export(context.Background(), path, "epoch")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func scalar(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var out string
	if err := db.QueryRow(q).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyPreservesPrivateStateAndPropagatesPolicy(t *testing.T) {
	src, source := fixture(t, "source-key")
	dst, target := fixture(t, "destination-key")
	_, err := source.Exec("UPDATE combos SET models='[\"p/b\",\"p/a\"]',matchRules='[{\"pattern\":\"p/*\"}]'")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec("INSERT INTO kv VALUES('customModels','p|new|llm','{\"id\":\"new\",\"providerAlias\":\"p\",\"contextWindow\":999}')")
	if err != nil {
		t.Fatal(err)
	}
	_, err = target.Exec("INSERT INTO combos VALUES('removed','obsolete','llm','[]','[]','old','old')")
	if err != nil {
		t.Fatal(err)
	}
	s := exportTest(t, src)
	if strings.Contains(string(mustJSON(t, s)), "local-refresh-source-key") {
		t.Fatal("OAuth credential leaked into export")
	}
	before := scalar(t, target, "SELECT data FROM providerConnections WHERE id='account'")
	beforeData, _ := decode(before)
	if err = Apply(context.Background(), dst, s, false); err != nil {
		t.Fatal(err)
	}
	afterData, _ := decode(scalar(t, target, "SELECT data FROM providerConnections WHERE id='account'"))
	for _, k := range []string{"apiKey", "refreshToken", "accessToken", "testStatus"} {
		if beforeData[k] != afterData[k] {
			t.Fatalf("local %s changed", k)
		}
	}
	if object(afterData["providerSpecificData"])["chatgptAccountId"] != "local-account-destination-key" {
		t.Fatal("local account binding changed")
	}
	if got := scalar(t, target, "SELECT key FROM apiKeys WHERE id='client'"); got != "local-client-key" {
		t.Fatal("client key changed")
	}
	if got := scalar(t, target, "SELECT promptTokens FROM usageHistory"); got != "12345" {
		t.Fatal("usage changed")
	}
	if got := scalar(t, target, "SELECT count(*) FROM combos WHERE id='removed'"); got != "0" {
		t.Fatal("deleted config resurrected")
	}
	if got := exportTest(t, dst); got.Hash != s.Hash {
		t.Fatal("managed states did not converge")
	}
	stamp := scalar(t, target, "SELECT updatedAt FROM combos WHERE id='chain'")
	if err = Apply(context.Background(), dst, s, false); err != nil {
		t.Fatal(err)
	}
	if scalar(t, target, "SELECT updatedAt FROM combos WHERE id='chain'") != stamp {
		t.Fatal("idempotent apply wrote unchanged rows")
	}
}
func TestDryRunAndInvalidSnapshotDoNotWrite(t *testing.T) {
	src, source := fixture(t, "source")
	dst, target := fixture(t, "local")
	_, _ = source.Exec("UPDATE combos SET name='changed'")
	before := scalar(t, target, "SELECT name FROM combos WHERE id='chain'")
	s := exportTest(t, src)
	if err := Apply(context.Background(), dst, s, true); err != nil {
		t.Fatal(err)
	}
	if scalar(t, target, "SELECT name FROM combos WHERE id='chain'") != before {
		t.Fatal("dry run committed")
	}
	s.Tables["providerConnections"][0]["data"].(Object)["proxyPoolId"] = "missing"
	s.Hash, _ = s.PolicyHash()
	if err := Apply(context.Background(), dst, s, false); err == nil {
		t.Fatal("invalid reference accepted")
	}
	if scalar(t, target, "SELECT name FROM combos WHERE id='chain'") != before {
		t.Fatal("invalid snapshot partially committed")
	}
}
func TestSealedCredentialsAndOAuthNeverReplaced(t *testing.T) {
	src, _ := fixture(t, "source")
	dst, target := fixture(t, "")
	_, _ = target.Exec("UPDATE providerConnections SET secretCiphertext='local-sealed',secretWrappedDEK='local-dek'")
	s := exportTest(t, src)
	s.Tables["combos"][0]["name"] = "new"
	s.Hash, _ = s.PolicyHash()
	if err := Apply(context.Background(), dst, s, false); err != nil {
		t.Fatal(err)
	}
	if scalar(t, target, "SELECT secretCiphertext FROM providerConnections") != "local-sealed" {
		t.Fatal("sealed credentials replaced")
	}
	data, _ := decode(scalar(t, target, "SELECT data FROM providerConnections"))
	if data["apiKey"] != "" {
		t.Fatal("plaintext key inserted over sealed key")
	}
}
func TestBackupIncludesWALAndRecoveryRestoresPair(t *testing.T) {
	path, db := fixture(t, "local")
	c := DeviceConfig{Root: t.TempDir(), DataDir: filepath.Dir(filepath.Dir(path))}
	before := Active{Commit: strings.Repeat("a", 40), Gateway: "old"}
	p, err := prepareBackup(context.Background(), c, before, Active{Commit: strings.Repeat("b", 40), Gateway: "new"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec("UPDATE combos SET name='broken'")
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = restore(c, p); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if scalar(t, restored, "SELECT name FROM combos") != "coding" {
		t.Fatal("WAL-aware backup/restore failed")
	}
	var a Active
	if err = ReadJSON(c.state("active.json"), &a); err != nil || a.Commit != before.Commit {
		t.Fatal("binary pointer not restored", err)
	}
}
func TestSignedJSONSurvivesStorageAndRejectsTampering(t *testing.T) {
	keyfile := filepath.Join(t.TempDir(), "key")
	pub, err := GenerateKey(keyfile)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(keyfile)
	s, err := Sign(Object{"revision": 1, "nested": Object{"x": "y"}}, string(key))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "signed.json")
	if err = WriteJSON(file, s); err != nil {
		t.Fatal(err)
	}
	var stored Signed
	if err = ReadJSON(file, &stored); err != nil {
		t.Fatal(err)
	}
	var out Object
	if err = stored.Verify(pub, &out); err != nil {
		t.Fatal(err)
	}
	stored.Payload = json.RawMessage(strings.Replace(string(stored.Payload), "\"y\"", "\"tampered\"", 1))
	if stored.Verify(pub, &out) == nil {
		t.Fatal("tampered config accepted")
	}
}
func TestUnknownPolicyFailsClosed(t *testing.T) {
	path, db := fixture(t, "local")
	_, _ = db.Exec("UPDATE settings SET data='{\"brandNewRoutingRule\":true}'")
	if _, err := Export(context.Background(), path, "epoch"); err == nil {
		t.Fatal("unclassified setting was silently dropped")
	}
}
func TestReleaseRejectsUpstreamAndWrongAssets(t *testing.T) {
	r := Release{Repository: Repository, Commit: strings.Repeat("a", 40), Schema: 1, Assets: map[string]Asset{"9router-go-linux-amd64": {URL: "https://github.com/luqman-v1/9router-go/releases/latest", SHA256: strings.Repeat("a", 64)}}}
	if r.Validate() == nil {
		t.Fatal("upstream download accepted")
	}
	r.Repository = "other/fork"
	if r.Validate() == nil {
		t.Fatal("wrong fork accepted")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
