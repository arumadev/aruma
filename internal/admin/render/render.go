// Package render turns the control plane's config data into downloadable files through
// configgen. The console and the Management API both use it, so they serve the same bytes.
package render

import (
	"github.com/osama300/aurma-project/internal/configgen"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// Project converts a project to configgen input.
func Project(p core.Project) configgen.Project {
	return configgen.Project{Number: p.Number, ID: p.ID, AuthDomain: p.AuthDomain, StorageBucket: p.StorageBucket, DatabaseURL: p.DatabaseURL}
}

// App converts a keyed app to configgen input.
func App(a core.KeyedApp) configgen.App {
	return configgen.App{ID: a.App.ID, PackageName: a.App.PackageName, BundleID: a.App.BundleID, APIKey: a.APIKey}
}

func app(a *core.KeyedApp) *configgen.App {
	if a == nil {
		return nil
	}
	out := App(*a)
	return &out
}

// File is a rendered config file.
type File struct {
	Name        string
	ContentType string
	Body        []byte
}

// AppFile renders an app's native config file: JSON for web, google-services.json for
// Android, GoogleService-Info.plist for iOS.
func AppFile(c core.AppConfig) File {
	p := Project(c.Project)
	switch c.App.App.Platform {
	case core.PlatformAndroid:
		apps := make([]configgen.App, 0, len(c.Apps))
		for _, a := range c.Apps {
			apps = append(apps, App(a))
		}
		return File{configgen.AndroidFileName, "application/json", configgen.Android(p, apps)}
	case core.PlatformIOS:
		return File{configgen.IOSFileName, "application/x-plist", configgen.IOS(p, App(c.App))}
	default:
		return File{configgen.WebFileName, "application/json", configgen.WebJSON(p, App(c.App))}
	}
}

// WebSnippet renders the JavaScript block for a web app.
func WebSnippet(c core.AppConfig) []byte { return configgen.WebSnippet(Project(c.Project), App(c.App)) }

// Flutter renders firebase_options.dart.
func Flutter(c core.FlutterConfig) File {
	body := configgen.Flutter(Project(c.Project), configgen.FlutterApps{
		Web: app(c.Web), Android: app(c.Android), IOS: app(c.IOS), MacOS: app(c.MacOS),
	})
	return File{configgen.FlutterFileName, "text/x-dart; charset=utf-8", body}
}

// ServiceAccountKey renders the key file of a freshly generated key.
func ServiceAccountKey(k core.GeneratedKey, publicURL string) File {
	body := configgen.ServiceAccountKeyFile(configgen.ServiceAccountKey{
		ProjectID: k.ServiceAccount.ProjectID, KeyID: k.Key.KeyID, PrivateKeyPEM: k.PrivateKeyPEM,
		ClientEmail: k.ServiceAccount.Email, ClientID: k.ServiceAccount.UniqueID, PublicURL: publicURL,
	})
	return File{k.ServiceAccount.ProjectID + "-" + k.Key.KeyID[:12] + ".json", "application/json", body}
}
