package fleet

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLocalLoginBindingPreservesManagedIdentityAndRouting(t *testing.T) {
	path, db := fixture(t, "old-local")
	_, err := db.Exec("INSERT INTO providerConnections(id,provider,authType,name,priority,isActive,data,createdAt,updatedAt) VALUES('extra-login','p','apikey','login-name',99,1,?,'now','now')", `{"apiKey":"new-local","refreshToken":"new-refresh","defaultModel":"wrong","providerSpecificData":{"enabledModels":["wrong"],"chatgptAccountId":"new-account"}}`)
	if err != nil {
		t.Fatal(err)
	}
	c := DeviceConfig{Role: Follower, Root: t.TempDir(), DataDir: filepath.Dir(filepath.Dir(path))}
	if err = WriteJSON(c.state("managed-connections.json"), []string{"account"}); err != nil {
		t.Fatal(err)
	}
	request := CredentialBinding{Source: "extra-login", Target: "account"}
	if err = QueueBinding(c, request); err != nil {
		t.Fatal(err)
	}
	if err = bindCredentials(context.Background(), path, request); err != nil {
		t.Fatal(err)
	}
	data, err := decode(scalar(t, db, "SELECT data FROM providerConnections WHERE id='account'"))
	if err != nil {
		t.Fatal(err)
	}
	if data["apiKey"] != "new-local" || data["refreshToken"] != "new-refresh" {
		t.Fatal("fresh device-local credentials were not bound")
	}
	if data["defaultModel"] != "a" || scalar(t, db, "SELECT name FROM providerConnections WHERE id='account'") != "account" {
		t.Fatal("local login overwrote shared definition")
	}
	if object(data["providerSpecificData"])["chatgptAccountId"] != "new-account" {
		t.Fatal("account binding not retained")
	}
	if scalar(t, db, "SELECT count(*) FROM providerConnections") != "1" {
		t.Fatal("extra login still causes configuration drift")
	}
	if scalar(t, db, "SELECT key FROM apiKeys") != "local-client-key" {
		t.Fatal("client authorization changed")
	}
}
func TestBindingRejectsAnotherProviderAndManagedSource(t *testing.T) {
	path, db := fixture(t, "local")
	_, err := db.Exec("INSERT INTO providerConnections(id,provider,authType,name,priority,isActive,data,createdAt,updatedAt) VALUES('extra','other','apikey','login',1,1,'{}','now','now')")
	if err != nil {
		t.Fatal(err)
	}
	if bindCredentials(context.Background(), path, CredentialBinding{Source: "extra", Target: "account"}) == nil {
		t.Fatal("cross-provider binding accepted")
	}
	if scalar(t, db, "SELECT count(*) FROM providerConnections") != "2" {
		t.Fatal("rejected binding partially committed")
	}
	c := DeviceConfig{Role: Follower, Root: t.TempDir()}
	if err = WriteJSON(c.state("managed-connections.json"), []string{"account", "extra"}); err != nil {
		t.Fatal(err)
	}
	if QueueBinding(c, CredentialBinding{Source: "extra", Target: "account"}) == nil {
		t.Fatal("managed connection accepted as disposable login row")
	}
}
