package mgmt

import (
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/tenancy/core"
)

// Resource shapes of firebase.googleapis.com/v1beta1 (docs/research/management-api.md).
// Names always use the project id, even when the request used the number or "-".

const typePrefix = "type.googleapis.com/google.firebase.service.v1beta1."

type firebaseProject struct {
	Name          string           `json:"name"`
	ProjectID     string           `json:"projectId"`
	ProjectNumber string           `json:"projectNumber"`
	DisplayName   string           `json:"displayName"`
	Resources     defaultResources `json:"resources"`
	State         string           `json:"state"`
	ETag          string           `json:"etag"`
}

type defaultResources struct {
	HostingSite   string `json:"hostingSite"`
	StorageBucket string `json:"storageBucket"`
	// RealtimeDatabaseInstance names the project's default database when the Realtime
	// Database is served: firebase-tools takes it as the instance to deploy rules to.
	RealtimeDatabaseInstance string `json:"realtimeDatabaseInstance,omitempty"`
}

func (a *API) toProject(p core.Project) firebaseProject {
	out := firebaseProject{
		Name: "projects/" + p.ID, ProjectID: p.ID, ProjectNumber: strconv.FormatInt(p.Number, 10),
		DisplayName: p.DisplayName, State: p.State, ETag: p.ETag,
		// hostingSite is always set so the firebase CLI never calls the Hosting API.
		Resources: defaultResources{HostingSite: p.HostingSite, StorageBucket: p.StorageBucket},
	}
	if a.opts.RTDB {
		out.Resources.RealtimeDatabaseInstance = p.ID + rtdbSuffix
	}
	return out
}

type projectInfo struct {
	Project     string `json:"project"`
	DisplayName string `json:"displayName"`
}

// collection is the URL segment of a platform's apps.
func collection(platform string) string {
	switch platform {
	case core.PlatformAndroid:
		return "androidApps"
	case core.PlatformIOS:
		return "iosApps"
	}
	return "webApps"
}

func appName(a core.App) string {
	return "projects/" + a.ProjectID + "/" + collection(a.Platform) + "/" + a.ID
}

func expireTime(a core.App) string {
	if a.ExpireTime == nil {
		return ""
	}
	return a.ExpireTime.UTC().Format(time.RFC3339)
}

func apiKeyID(a core.App) string {
	if a.APIKeyUID == nil {
		return ""
	}
	return a.APIKeyUID.String()
}

type webApp struct {
	Name        string   `json:"name"`
	AppID       string   `json:"appId"`
	DisplayName string   `json:"displayName,omitempty"`
	ProjectID   string   `json:"projectId"`
	AppURLs     []string `json:"appUrls"`
	WebID       string   `json:"webId"`
	APIKeyID    string   `json:"apiKeyId,omitempty"`
	State       string   `json:"state"`
	ExpireTime  string   `json:"expireTime,omitempty"`
	ETag        string   `json:"etag"`
}

type iosApp struct {
	Name        string `json:"name"`
	AppID       string `json:"appId"`
	DisplayName string `json:"displayName,omitempty"`
	ProjectID   string `json:"projectId"`
	BundleID    string `json:"bundleId"`
	APIKeyID    string `json:"apiKeyId,omitempty"`
	State       string `json:"state"`
	ExpireTime  string `json:"expireTime,omitempty"`
	ETag        string `json:"etag"`
}

type androidApp struct {
	Name         string   `json:"name"`
	AppID        string   `json:"appId"`
	DisplayName  string   `json:"displayName,omitempty"`
	ProjectID    string   `json:"projectId"`
	PackageName  string   `json:"packageName"`
	SHA1Hashes   []string `json:"sha1Hashes"`
	SHA256Hashes []string `json:"sha256Hashes"`
	APIKeyID     string   `json:"apiKeyId,omitempty"`
	State        string   `json:"state"`
	ExpireTime   string   `json:"expireTime,omitempty"`
	ETag         string   `json:"etag"`
}

// appResource renders an app in its platform's shape, with its @type for operation responses.
func appResource(a core.App, shas []core.ShaCertificate) (string, any) {
	switch a.Platform {
	case core.PlatformAndroid:
		out := androidApp{
			Name: appName(a), AppID: a.ID, DisplayName: a.DisplayName, ProjectID: a.ProjectID, PackageName: a.PackageName,
			SHA1Hashes: []string{}, SHA256Hashes: []string{}, APIKeyID: apiKeyID(a), State: a.State, ExpireTime: expireTime(a), ETag: a.ETag,
		}
		for _, s := range shas {
			if s.CertType == "SHA_1" {
				out.SHA1Hashes = append(out.SHA1Hashes, s.Hash)
			} else {
				out.SHA256Hashes = append(out.SHA256Hashes, s.Hash)
			}
		}
		return "AndroidApp", out
	case core.PlatformIOS:
		return "IosApp", iosApp{
			Name: appName(a), AppID: a.ID, DisplayName: a.DisplayName, ProjectID: a.ProjectID, BundleID: a.BundleID,
			APIKeyID: apiKeyID(a), State: a.State, ExpireTime: expireTime(a), ETag: a.ETag,
		}
	}
	return "WebApp", webApp{
		Name: appName(a), AppID: a.ID, DisplayName: a.DisplayName, ProjectID: a.ProjectID, AppURLs: []string{},
		WebID: a.Namespace(), APIKeyID: apiKeyID(a), State: a.State, ExpireTime: expireTime(a), ETag: a.ETag,
	}
}

type appInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Platform    string `json:"platform"`
	AppID       string `json:"appId"`
	Namespace   string `json:"namespace"`
	APIKeyID    string `json:"apiKeyId,omitempty"`
	State       string `json:"state"`
	ExpireTime  string `json:"expireTime,omitempty"`
}

func toAppInfo(a core.App) appInfo {
	return appInfo{
		Name: appName(a), DisplayName: a.DisplayName, Platform: a.Platform, AppID: a.ID, Namespace: a.Namespace(),
		APIKeyID: apiKeyID(a), State: a.State, ExpireTime: expireTime(a),
	}
}

type shaCertificate struct {
	Name     string `json:"name"`
	ShaHash  string `json:"shaHash"`
	CertType string `json:"certType"`
}

func toSha(projectID, appID string, c core.ShaCertificate) shaCertificate {
	return shaCertificate{Name: "projects/" + projectID + "/androidApps/" + appID + "/sha/" + c.ID.String(), ShaHash: c.Hash, CertType: c.CertType}
}

type nativeConfig struct {
	ConfigFilename     string `json:"configFilename"`
	ConfigFileContents []byte `json:"configFileContents"` // encoding/json writes standard base64
}

// operation is a google.longrunning.Operation. Aruma's operations complete before the
// response is sent, so done is always true.
type operation struct {
	Name     string `json:"name"`
	Done     bool   `json:"done"`
	Response any    `json:"response,omitempty"`
}
