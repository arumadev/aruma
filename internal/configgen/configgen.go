// Package configgen renders the configuration files developers download, in the exact shapes
// the official client and Admin SDKs read (docs/research/client-config-formats.md).
//
// It is pure: values in, bytes out. Files are generated on request and never stored, and every
// API that serves a file calls the same function, so the Management API and the console return
// identical bytes (AC-21). Every file carries every field from day one, including those of
// services that arrive in later phases, so developers never need to download it again.
package configgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Project is the project-level input.
type Project struct {
	Number        int64
	ID            string
	AuthDomain    string
	StorageBucket string
	DatabaseURL   string
}

func (p Project) number() string { return strconv.FormatInt(p.Number, 10) }

// App is one registered app and the API key its config carries.
type App struct {
	ID          string
	PackageName string // Android
	BundleID    string // iOS
	APIKey      string
}

// WebConfig is the web firebaseConfig object (the Management API's WebAppConfig).
type WebConfig struct {
	ProjectID         string `json:"projectId"`
	AppID             string `json:"appId"`
	DatabaseURL       string `json:"databaseURL"`
	StorageBucket     string `json:"storageBucket"`
	APIKey            string `json:"apiKey"`
	AuthDomain        string `json:"authDomain"`
	MessagingSenderID string `json:"messagingSenderId"`
	ProjectNumber     string `json:"projectNumber"`
}

// Web returns the web app config.
func Web(p Project, a App) WebConfig {
	return WebConfig{
		ProjectID: p.ID, AppID: a.ID, DatabaseURL: p.DatabaseURL, StorageBucket: p.StorageBucket,
		APIKey: a.APIKey, AuthDomain: p.AuthDomain, MessagingSenderID: p.number(), ProjectNumber: p.number(),
	}
}

// WebJSON renders the web config as a JSON document.
func WebJSON(p Project, a App) []byte { return marshal(Web(p, a)) }

// WebSnippet renders the config as the JavaScript block developers paste into their app.
func WebSnippet(p Project, a App) []byte {
	c := Web(p, a)
	fields := []struct{ k, v string }{
		{"apiKey", c.APIKey}, {"authDomain", c.AuthDomain}, {"databaseURL", c.DatabaseURL},
		{"projectId", c.ProjectID}, {"storageBucket", c.StorageBucket},
		{"messagingSenderId", c.MessagingSenderID}, {"appId", c.AppID},
	}
	var b bytes.Buffer
	b.WriteString("const firebaseConfig = {\n")
	for _, f := range fields {
		v, _ := json.Marshal(f.v)
		fmt.Fprintf(&b, "  %s: %s,\n", f.k, v)
	}
	b.WriteString("};\n")
	return b.Bytes()
}

// Android renders google-services.json for every Android app of a project, in the given
// order. The Gradle plugin picks the client whose package name matches the build.
func Android(p Project, apps []App) []byte {
	type clientInfo struct {
		MobileSDKAppID    string `json:"mobilesdk_app_id"`
		AndroidClientInfo struct {
			PackageName string `json:"package_name"`
		} `json:"android_client_info"`
	}
	type client struct {
		ClientInfo  clientInfo `json:"client_info"`
		OAuthClient []any      `json:"oauth_client"`
		APIKey      []struct {
			CurrentKey string `json:"current_key"`
		} `json:"api_key"`
		Services struct {
			AppInviteService struct {
				OtherPlatformOAuthClient []any `json:"other_platform_oauth_client"`
			} `json:"appinvite_service"`
		} `json:"services"`
	}
	doc := struct {
		ProjectInfo struct {
			ProjectNumber string `json:"project_number"`
			FirebaseURL   string `json:"firebase_url"`
			ProjectID     string `json:"project_id"`
			StorageBucket string `json:"storage_bucket"`
		} `json:"project_info"`
		Client               []client `json:"client"`
		ConfigurationVersion string   `json:"configuration_version"`
	}{ConfigurationVersion: "1", Client: []client{}}
	doc.ProjectInfo.ProjectNumber = p.number()
	doc.ProjectInfo.FirebaseURL = p.DatabaseURL
	doc.ProjectInfo.ProjectID = p.ID
	doc.ProjectInfo.StorageBucket = p.StorageBucket
	for _, a := range apps {
		var c client
		c.ClientInfo.MobileSDKAppID = a.ID
		c.ClientInfo.AndroidClientInfo.PackageName = a.PackageName
		c.OAuthClient = []any{}
		c.APIKey = []struct {
			CurrentKey string `json:"current_key"`
		}{{a.APIKey}}
		c.Services.AppInviteService.OtherPlatformOAuthClient = []any{}
		doc.Client = append(doc.Client, c)
	}
	return marshal(doc)
}

// IOS renders GoogleService-Info.plist for one iOS app.
func IOS(p Project, a App) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	str := func(k, v string) { fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", k, xmlEscape(v)) }
	boolean := func(k string, v bool) { fmt.Fprintf(&b, "\t<key>%s</key>\n\t<%t></%t>\n", k, v, v) }
	str("API_KEY", a.APIKey)
	str("GCM_SENDER_ID", p.number())
	str("PLIST_VERSION", "1")
	str("BUNDLE_ID", a.BundleID)
	str("PROJECT_ID", p.ID)
	str("STORAGE_BUCKET", p.StorageBucket)
	boolean("IS_ADS_ENABLED", false)
	boolean("IS_ANALYTICS_ENABLED", false)
	boolean("IS_APPINVITE_ENABLED", true)
	boolean("IS_GCM_ENABLED", true)
	boolean("IS_SIGNIN_ENABLED", true)
	str("GOOGLE_APP_ID", a.ID)
	str("DATABASE_URL", p.DatabaseURL)
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

// FlutterApps selects one app per platform for firebase_options.dart; nil platforms are left
// unconfigured. macOS uses an iOS app, as the FlutterFire CLI does.
type FlutterApps struct {
	Web, Android, IOS, MacOS *App
}

// Flutter renders lib/firebase_options.dart in the shape the FlutterFire CLI writes.
func Flutter(p Project, apps FlutterApps) []byte {
	var b bytes.Buffer
	b.WriteString(`// File generated by Aruma in the FlutterFire CLI format.
// ignore_for_file: type=lint
import 'package:firebase_core/firebase_core.dart' show FirebaseOptions;
import 'package:flutter/foundation.dart'
    show defaultTargetPlatform, kIsWeb, TargetPlatform;

/// Default [FirebaseOptions] for use with your Aruma apps.
///
/// Example:
/// ` + "```dart" + `
/// import 'firebase_options.dart';
/// // ...
/// await Firebase.initializeApp(
///   options: DefaultFirebaseOptions.currentPlatform,
/// );
/// ` + "```" + `
class DefaultFirebaseOptions {
  static FirebaseOptions get currentPlatform {
`)
	if apps.Web != nil {
		b.WriteString("    if (kIsWeb) {\n      return web;\n    }\n")
	} else {
		b.WriteString("    if (kIsWeb) {\n" + unsupported("      ", "web") + "    }\n")
	}
	b.WriteString("    switch (defaultTargetPlatform) {\n")
	for _, c := range []struct {
		target, name string
		app          *App
	}{
		{"android", "android", apps.Android},
		{"iOS", "ios", apps.IOS},
		{"macOS", "macos", apps.MacOS},
		{"windows", "windows", nil},
		{"linux", "linux", nil},
	} {
		fmt.Fprintf(&b, "      case TargetPlatform.%s:\n", c.target)
		if c.app != nil {
			fmt.Fprintf(&b, "        return %s;\n", c.name)
		} else {
			b.WriteString(unsupported("        ", c.name))
		}
	}
	b.WriteString(`      default:
        throw UnsupportedError(
          'DefaultFirebaseOptions are not supported for this platform.',
        );
    }
  }
`)
	type field struct{ k, v string }
	write := func(name string, fields []field) {
		fmt.Fprintf(&b, "\n  static const FirebaseOptions %s = FirebaseOptions(\n", name)
		for _, f := range fields {
			if f.v != "" {
				fmt.Fprintf(&b, "    %s: %s,\n", f.k, dartString(f.v))
			}
		}
		b.WriteString("  );\n")
	}
	common := func(a *App) []field {
		return []field{{"apiKey", a.APIKey}, {"appId", a.ID}, {"messagingSenderId", p.number()}, {"projectId", p.ID}}
	}
	if a := apps.Web; a != nil {
		write("web", append(common(a), field{"authDomain", p.AuthDomain}, field{"databaseURL", p.DatabaseURL}, field{"storageBucket", p.StorageBucket}))
	}
	if a := apps.Android; a != nil {
		write("android", append(common(a), field{"databaseURL", p.DatabaseURL}, field{"storageBucket", p.StorageBucket}))
	}
	if a := apps.IOS; a != nil {
		write("ios", append(common(a), field{"databaseURL", p.DatabaseURL}, field{"storageBucket", p.StorageBucket}, field{"iosBundleId", a.BundleID}))
	}
	if a := apps.MacOS; a != nil {
		write("macos", append(common(a), field{"databaseURL", p.DatabaseURL}, field{"storageBucket", p.StorageBucket}, field{"iosBundleId", a.BundleID}))
	}
	b.WriteString("}\n")
	return b.Bytes()
}

func unsupported(indent, platform string) string {
	hint := map[string]string{
		"web":     "add a web app in the Aruma console and download this file again.",
		"android": "add an Android app in the Aruma console and download this file again.",
		"ios":     "add an iOS app in the Aruma console and download this file again.",
		"macos":   "choose an iOS app for macOS in the Aruma console and download this file again.",
	}[platform]
	if hint == "" {
		hint = "Aruma has no " + platform + " apps."
	}
	return indent + "throw UnsupportedError(\n" +
		indent + "  'DefaultFirebaseOptions have not been configured for " + platform + " - '\n" +
		indent + "  '" + hint + "',\n" +
		indent + ");\n"
}

// ServiceAccountKey is the input for a service account key file.
type ServiceAccountKey struct {
	ProjectID     string
	KeyID         string
	PrivateKeyPEM string
	ClientEmail   string
	ClientID      string
	// PublicURL is the server's base URL; every endpoint in the file points at Aruma.
	PublicURL string
}

// ServiceAccountKeyFile renders the key file Admin SDKs load with cert(...) or
// GOOGLE_APPLICATION_CREDENTIALS, field for field like Google's. universe_domain is left out:
// its default is the only value that keeps libraries on the token exchange.
func ServiceAccountKeyFile(k ServiceAccountKey) []byte {
	base := strings.TrimSuffix(k.PublicURL, "/")
	return marshal(struct {
		Type                    string `json:"type"`
		ProjectID               string `json:"project_id"`
		PrivateKeyID            string `json:"private_key_id"`
		PrivateKey              string `json:"private_key"`
		ClientEmail             string `json:"client_email"`
		ClientID                string `json:"client_id"`
		AuthURI                 string `json:"auth_uri"`
		TokenURI                string `json:"token_uri"`
		AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
		ClientX509CertURL       string `json:"client_x509_cert_url"`
	}{
		Type: "service_account", ProjectID: k.ProjectID, PrivateKeyID: k.KeyID, PrivateKey: k.PrivateKeyPEM,
		ClientEmail: k.ClientEmail, ClientID: k.ClientID,
		AuthURI:                 base + "/oauth2/v1/auth",
		TokenURI:                TokenURL(base),
		AuthProviderX509CertURL: base + "/oauth2/v1/certs",
		ClientX509CertURL:       base + "/oauth2/v1/x509/" + url.PathEscape(k.ClientEmail),
	})
}

// TokenURL is the service account token endpoint for a server base URL.
func TokenURL(publicURL string) string {
	return strings.TrimSuffix(publicURL, "/") + "/oauth2/v1/token"
}

// AdminSDKConfig is the adminSdkConfig object.
type AdminSDKConfig struct {
	ProjectID     string `json:"projectId"`
	DatabaseURL   string `json:"databaseURL"`
	StorageBucket string `json:"storageBucket"`
}

// Admin returns the Admin SDK config of a project.
func Admin(p Project) AdminSDKConfig {
	return AdminSDKConfig{ProjectID: p.ID, DatabaseURL: p.DatabaseURL, StorageBucket: p.StorageBucket}
}

// File names, as the SDKs and tools expect them.
const (
	AndroidFileName = "google-services.json"
	IOSFileName     = "GoogleService-Info.plist"
	FlutterFileName = "firebase_options.dart"
	WebFileName     = "firebase-config.json"
)

func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return b.Bytes()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dartString quotes s as a single-quoted Dart literal.
func dartString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, `$`, `\$`, "\n", `\n`)
	return "'" + r.Replace(s) + "'"
}
