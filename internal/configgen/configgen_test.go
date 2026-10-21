package configgen

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/arumadev/aruma/internal/configgen/schema"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	project = Project{
		Number: 482915736204, ID: "chat-app", AuthDomain: "aruma.example.com",
		StorageBucket: "chat-app.aruma.example.com", DatabaseURL: "https://chat-app-default-rtdb.rtdb.aruma.example.com",
	}
	web      = App{ID: "1:482915736204:web:3f9a1c7e2b4d8a60", APIKey: "AruQ7mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o"}
	android  = App{ID: "1:482915736204:android:0a1b2c3d4e5f6071", PackageName: "com.ahmad.chat", APIKey: "AruP2mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o"}
	android2 = App{ID: "1:482915736204:android:8a1b2c3d4e5f6072", PackageName: "com.ahmad.chat.admin", APIKey: "AruP2mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o"}
	ios      = App{ID: "1:482915736204:ios:1a2b3c4d5e6f7081", BundleID: "com.ahmad.chat", APIKey: "AruK9mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o"}
)

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden file:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func validate(t *testing.T, schemaFile string, doc []byte) {
	t.Helper()
	c := jsonschema.NewCompiler()
	f, err := schema.FS.Open(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource(schemaFile, s); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("not JSON: %v\n%s", err, doc)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("%s: %v\n%s", schemaFile, err, doc)
	}
}

func TestWeb(t *testing.T) {
	doc := WebJSON(project, web)
	validate(t, "web-config.schema.json", doc)
	golden(t, "web-config.json", doc)
	golden(t, "web-snippet.js", WebSnippet(project, web))
}

func TestAndroid(t *testing.T) {
	doc := Android(project, []App{android, android2})
	validate(t, "google-services.schema.json", doc)
	golden(t, "google-services.json", doc)
	if !bytes.Equal(Android(project, nil), Android(project, []App{})) {
		t.Fatal("nil and empty app lists differ")
	}
	validate(t, "google-services.schema.json", Android(project, nil))
}

func TestIOS(t *testing.T) {
	doc := IOS(project, ios)
	golden(t, "GoogleService-Info.plist", doc)

	// The plist is well-formed XML and every value round-trips.
	dec := xml.NewDecoder(bytes.NewReader(doc))
	values := map[string]string{}
	var key string
	inKey := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			inKey = tok.Name.Local == "key"
			if tok.Name.Local == "true" || tok.Name.Local == "false" {
				values[key] = tok.Name.Local
			}
		case xml.CharData:
			if strings.TrimSpace(string(tok)) == "" {
				continue
			}
			if inKey {
				key = string(tok)
			} else {
				values[key] = string(tok)
			}
		}
	}
	want := map[string]string{
		"API_KEY": ios.APIKey, "GCM_SENDER_ID": "482915736204", "PLIST_VERSION": "1", "BUNDLE_ID": "com.ahmad.chat",
		"PROJECT_ID": "chat-app", "GOOGLE_APP_ID": ios.ID, "IS_ANALYTICS_ENABLED": "false",
		"STORAGE_BUCKET": project.StorageBucket, "DATABASE_URL": project.DatabaseURL,
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q, want %q", k, values[k], v)
		}
	}
}

func TestPlistEscaping(t *testing.T) {
	doc := IOS(Project{ID: "x", DatabaseURL: "https://h/?ns=a&b=<c>"}, App{})
	if !strings.Contains(string(doc), "https://h/?ns=a&amp;b=&lt;c&gt;") {
		t.Fatalf("not escaped:\n%s", doc)
	}
}

func TestFlutter(t *testing.T) {
	golden(t, "firebase_options.dart", Flutter(project, FlutterApps{Web: &web, Android: &android, IOS: &ios}))
	partial := string(Flutter(project, FlutterApps{Android: &android}))
	for _, want := range []string{"have not been configured for web", "have not been configured for ios", "return android;"} {
		if !strings.Contains(partial, want) {
			t.Errorf("partial options lack %q", want)
		}
	}
	if strings.Contains(partial, "static const FirebaseOptions web") {
		t.Error("unconfigured platform still declared")
	}
	if got := dartString(`it's $x \ y`); got != `'it\'s \$x \\ y'` {
		t.Errorf("dartString = %s", got)
	}
}

func TestServiceAccountKeyFile(t *testing.T) {
	doc := ServiceAccountKeyFile(ServiceAccountKey{
		ProjectID: "chat-app", KeyID: strings.Repeat("9c", 20),
		PrivateKeyPEM: "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n",
		ClientEmail:   "backend@chat-app.sa.aruma.example.com", ClientID: "112233445566778899001",
		PublicURL: "https://aruma.example.com/",
	})
	validate(t, "service-account-key.schema.json", doc)
	golden(t, "service-account.json", doc)
	var m map[string]string
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if strings.Contains(v, "googleapis.com") || strings.Contains(v, "google.com") {
			t.Errorf("%s points at Google: %s", k, v)
		}
	}
	if _, ok := m["universe_domain"]; ok {
		t.Error("universe_domain must be omitted")
	}
}

func TestAdmin(t *testing.T) {
	validate(t, "admin-sdk-config.schema.json", marshal(Admin(project)))
}
