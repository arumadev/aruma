package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type project struct {
	ProjectID     string `json:"projectId"`
	ProjectNumber string `json:"projectNumber"`
	DisplayName   string `json:"displayName"`
	State         string `json:"state"`
}

type app struct {
	AppID       string `json:"appId"`
	Platform    string `json:"platform"`
	DisplayName string `json:"displayName"`
	PackageName string `json:"packageName"`
	BundleID    string `json:"bundleId"`
	State       string `json:"state"`
}

func (a app) namespace() string {
	if a.PackageName != "" {
		return a.PackageName
	}
	return a.BundleID
}

type apiKey struct {
	UID         string `json:"uid"`
	KeyString   string `json:"keyString"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`
	Platform    string `json:"platform"`
}

type serviceAccount struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

func one(pos []string, what string) (string, error) {
	if len(pos) != 1 {
		return "", fmt.Errorf("expected exactly one %s", what)
	}
	return pos[0], nil
}

func projectFlag(fs *flag.FlagSet, p *string) {
	fs.StringVar(p, "project", "", "project id (required)")
}

func requireProject(p string) error {
	if p == "" {
		return errors.New("--project is required")
	}
	return nil
}

func (c *cli) projectsList(api *client) error {
	var out struct {
		Projects []project `json:"projects"`
	}
	if err := api.call("GET", "/projects", url.Values{"pageSize": {"1000"}}, nil, &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out.Projects))
	for _, p := range out.Projects {
		rows = append(rows, []string{p.ProjectID, p.ProjectNumber, p.DisplayName})
	}
	return c.print(out, rows, "PROJECT ID", "NUMBER", "NAME")
}

func (c *cli) projectsCreate(api *client, args []string) error {
	var name string
	pos, err := c.flags("projects create", args, func(fs *flag.FlagSet) { fs.StringVar(&name, "name", "", "display name") })
	if err != nil {
		return err
	}
	id, err := one(pos, "project id")
	if err != nil {
		return err
	}
	if name == "" {
		name = id
	}
	var p project
	if err := api.call("POST", "/projects", nil, map[string]string{"projectId": id, "displayName": name}, &p); err != nil {
		return err
	}
	return c.print(p, [][]string{{p.ProjectID, p.ProjectNumber, p.DisplayName}}, "PROJECT ID", "NUMBER", "NAME")
}

func (c *cli) projectsGet(api *client, args []string) error {
	id, err := one(args, "project id")
	if err != nil {
		return err
	}
	var p project
	if err := api.call("GET", "/projects/"+url.PathEscape(id), nil, nil, &p); err != nil {
		return err
	}
	return c.print(p, [][]string{{p.ProjectID, p.ProjectNumber, p.DisplayName, p.State}}, "PROJECT ID", "NUMBER", "NAME", "STATE")
}

func (c *cli) appsList(api *client, args []string) error {
	var proj, platform string
	if _, err := c.flags("apps list", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&platform, "platform", "", "web, ios or android")
	}); err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	q := url.Values{"pageSize": {"1000"}}
	if platform != "" {
		q.Set("platform", strings.ToUpper(platform))
	}
	var out struct {
		Apps []app `json:"apps"`
	}
	if err := api.call("GET", "/projects/"+url.PathEscape(proj)+"/apps", q, nil, &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out.Apps))
	for _, a := range out.Apps {
		rows = append(rows, []string{a.AppID, a.Platform, a.namespace(), a.DisplayName})
	}
	return c.print(out, rows, "APP ID", "PLATFORM", "PACKAGE/BUNDLE", "NAME")
}

func (c *cli) appsCreate(api *client, args []string) error {
	var proj, platform, name, pkg, bundle string
	if _, err := c.flags("apps create", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&platform, "platform", "", "web, ios or android (required)")
		fs.StringVar(&name, "name", "", "display name")
		fs.StringVar(&pkg, "package", "", "Android package name")
		fs.StringVar(&bundle, "bundle", "", "iOS bundle ID")
	}); err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	var a app
	body := map[string]string{"platform": strings.ToUpper(platform), "displayName": name, "packageName": pkg, "bundleId": bundle}
	if err := api.call("POST", "/projects/"+url.PathEscape(proj)+"/apps", nil, body, &a); err != nil {
		return err
	}
	return c.print(a, [][]string{{a.AppID, a.Platform, a.namespace(), a.DisplayName}}, "APP ID", "PLATFORM", "PACKAGE/BUNDLE", "NAME")
}

// appsSDKConfig prints the config file, or saves it with --out (a directory keeps the
// server's file name).
func (c *cli) appsSDKConfig(api *client, args []string) error {
	var proj, out string
	pos, err := c.flags("apps sdkconfig", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&out, "out", "", "file or directory to save to")
	})
	if err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	appID, err := one(pos, "app id")
	if err != nil {
		return err
	}
	r, err := api.do("GET", "/projects/"+url.PathEscape(proj)+"/apps/"+url.PathEscape(appID)+"/config", nil, nil)
	if err != nil {
		return err
	}
	if out == "" {
		_, err := c.stdout.Write(r.body)
		return err
	}
	return c.save(out, r, 0o644)
}

func (c *cli) save(out string, r *response, mode os.FileMode) error {
	if fi, err := os.Stat(out); err == nil && fi.IsDir() && r.filename != "" {
		out = strings.TrimSuffix(out, "/") + "/" + r.filename
	}
	if err := os.WriteFile(out, r.body, mode); err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "saved %s\n", out)
	return nil
}

func (c *cli) keysList(api *client, args []string) error {
	var proj string
	if _, err := c.flags("apikeys list", args, func(fs *flag.FlagSet) { projectFlag(fs, &proj) }); err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	var out struct {
		Keys []apiKey `json:"keys"`
	}
	if err := api.call("GET", "/projects/"+url.PathEscape(proj)+"/apiKeys", nil, nil, &out); err != nil {
		return err
	}
	rows := make([][]string, 0, len(out.Keys))
	for _, k := range out.Keys {
		rows = append(rows, []string{k.UID, k.KeyString, k.Platform, k.State, k.DisplayName})
	}
	return c.print(out, rows, "UID", "KEY", "PLATFORM", "STATE", "NAME")
}

func (c *cli) keysCreate(api *client, args []string) error {
	var proj, name string
	if _, err := c.flags("apikeys create", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&name, "name", "", "display name")
	}); err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	var k apiKey
	if err := api.call("POST", "/projects/"+url.PathEscape(proj)+"/apiKeys", nil, map[string]string{"displayName": name}, &k); err != nil {
		return err
	}
	return c.print(k, [][]string{{k.UID, k.KeyString, k.State, k.DisplayName}}, "UID", "KEY", "STATE", "NAME")
}

func (c *cli) keysRotate(api *client, args []string) error {
	var proj string
	pos, err := c.flags("apikeys rotate", args, func(fs *flag.FlagSet) { projectFlag(fs, &proj) })
	if err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	uid, err := one(pos, "key uid")
	if err != nil {
		return err
	}
	var k apiKey
	if err := api.call("POST", "/projects/"+url.PathEscape(proj)+"/apiKeys/"+url.PathEscape(uid)+":rotate", nil, nil, &k); err != nil {
		return err
	}
	if !c.json {
		fmt.Fprintln(c.stderr, "The old key keeps working until you disable it in the console.")
	}
	return c.print(k, [][]string{{k.UID, k.KeyString, k.Platform, k.State}}, "UID", "KEY", "PLATFORM", "STATE")
}

func (c *cli) saList(api *client, args []string) error {
	var proj string
	if _, err := c.flags("sa list", args, func(fs *flag.FlagSet) { projectFlag(fs, &proj) }); err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	list, err := c.serviceAccounts(api, proj)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(list))
	for _, s := range list {
		rows = append(rows, []string{s.ID, s.Email, s.DisplayName})
	}
	return c.print(map[string]any{"serviceAccounts": list}, rows, "ID", "EMAIL", "NAME")
}

func (c *cli) serviceAccounts(api *client, proj string) ([]serviceAccount, error) {
	var out struct {
		ServiceAccounts []serviceAccount `json:"serviceAccounts"`
	}
	err := api.call("GET", "/projects/"+url.PathEscape(proj)+"/serviceAccounts", nil, nil, &out)
	return out.ServiceAccounts, err
}

func (c *cli) saCreate(api *client, args []string) error {
	var proj, name string
	pos, err := c.flags("sa create", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&name, "name", "", "display name")
	})
	if err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	id, err := one(pos, "account id")
	if err != nil {
		return err
	}
	var s serviceAccount
	if err := api.call("POST", "/projects/"+url.PathEscape(proj)+"/serviceAccounts", nil, map[string]string{"accountId": id, "displayName": name}, &s); err != nil {
		return err
	}
	return c.print(s, [][]string{{s.ID, s.Email, s.DisplayName}}, "ID", "EMAIL", "NAME")
}

// saKeysCreate saves a new key file. It is written with mode 0600: it holds a private key that
// exists nowhere else.
func (c *cli) saKeysCreate(api *client, args []string) error {
	var proj, out string
	pos, err := c.flags("sa keys create", args, func(fs *flag.FlagSet) {
		projectFlag(fs, &proj)
		fs.StringVar(&out, "out", "", "file or directory to save the key file to (required)")
	})
	if err != nil {
		return err
	}
	if err := requireProject(proj); err != nil {
		return err
	}
	if out == "" {
		return errors.New("--out is required: the key file is shown only once")
	}
	ref, err := one(pos, "service account id or email")
	if err != nil {
		return err
	}
	id := ref
	if strings.Contains(ref, "@") {
		list, err := c.serviceAccounts(api, proj)
		if err != nil {
			return err
		}
		id = ""
		for _, s := range list {
			if s.Email == ref {
				id = s.ID
			}
		}
		if id == "" {
			return fmt.Errorf("no service account %s in project %s", ref, proj)
		}
	}
	r, err := api.do("POST", "/projects/"+url.PathEscape(proj)+"/serviceAccounts/"+url.PathEscape(id)+"/keys", nil, nil)
	if err != nil {
		return err
	}
	if err := c.save(out, r, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(c.stderr, "Keep this file safe: Aruma does not store the private key and cannot show it again.")
	return nil
}
