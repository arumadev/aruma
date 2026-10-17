package core

import (
	"context"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/tenancy/queries"
)

// KeyedApp is an app together with the API key its config file carries.
type KeyedApp struct {
	App    App
	APIKey string
}

// AppConfig is everything needed to render one app's config file. For Android, Apps lists
// every active Android app of the project (google-services.json holds them all), the requested
// one included; for other platforms it holds the requested app only.
type AppConfig struct {
	Project Project
	App     KeyedApp
	Apps    []KeyedApp
}

// AppConfig gathers the data for an app's config file. Files are rendered on request from the
// tables, never stored.
func (s *Service) AppConfig(ctx context.Context, p *authn.Principal, projectRef, appID string) (AppConfig, error) {
	proj, app, err := s.resolveApp(ctx, s.q, p, projectRef, appID, false)
	if err != nil {
		return AppConfig{}, err
	}
	key, err := s.appKey(ctx, s.q, app)
	if err != nil {
		return AppConfig{}, err
	}
	out := AppConfig{Project: s.toProject(proj), App: KeyedApp{toApp(app, proj.ProjectID), key}}
	if app.Platform != PlatformAndroid {
		out.Apps = []KeyedApp{out.App}
		return out, nil
	}
	rows, err := s.q.ListActiveAppsByPlatform(ctx, queries.ListActiveAppsByPlatformParams{ProjectNumber: proj.ProjectNumber, Platform: PlatformAndroid})
	if err != nil {
		return AppConfig{}, internal(err)
	}
	for _, r := range rows {
		k, err := s.appKey(ctx, s.q, r)
		if err != nil {
			return AppConfig{}, err
		}
		out.Apps = append(out.Apps, KeyedApp{toApp(r, proj.ProjectID), k})
	}
	return out, nil
}

// FlutterSelection names the app to use per platform; empty means unconfigured.
type FlutterSelection struct {
	Web, Android, IOS, MacOS string
}

// FlutterConfig gathers the apps chosen for firebase_options.dart.
type FlutterConfig struct {
	Project                  Project
	Web, Android, IOS, MacOS *KeyedApp
}

// FlutterConfig resolves the selected apps, checking each belongs to the project and platform.
func (s *Service) FlutterConfig(ctx context.Context, p *authn.Principal, projectRef string, sel FlutterSelection) (FlutterConfig, error) {
	proj, err := s.resolveProject(ctx, s.q, p, projectRef, false)
	if err != nil {
		return FlutterConfig{}, err
	}
	out := FlutterConfig{Project: s.toProject(proj)}
	pick := func(field, appID, platform string) (*KeyedApp, error) {
		if appID == "" {
			return nil, nil
		}
		app, err := s.q.GetApp(ctx, queries.GetAppParams{ProjectNumber: proj.ProjectNumber, AppID: appID})
		if err != nil || app.State != "ACTIVE" || app.Platform != platform {
			if err != nil && !notFound(err) {
				return nil, internal(err)
			}
			return nil, apierr.Invalid(field, "No active "+platform+" app with this ID in the project.")
		}
		key, err := s.appKey(ctx, s.q, app)
		if err != nil {
			return nil, err
		}
		return &KeyedApp{toApp(app, proj.ProjectID), key}, nil
	}
	if out.Web, err = pick("web", sel.Web, PlatformWeb); err != nil {
		return out, err
	}
	if out.Android, err = pick("android", sel.Android, PlatformAndroid); err != nil {
		return out, err
	}
	if out.IOS, err = pick("ios", sel.IOS, PlatformIOS); err != nil {
		return out, err
	}
	if out.MacOS, err = pick("macos", sel.MacOS, PlatformIOS); err != nil {
		return out, err
	}
	if out.Web == nil && out.Android == nil && out.IOS == nil && out.MacOS == nil {
		return out, apierr.Invalid("apps", "Choose at least one app.")
	}
	return out, nil
}
