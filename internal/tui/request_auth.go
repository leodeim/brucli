package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/runner"
)

func (m *Model) pickAuthMode() tea.Cmd {
	r := m.current
	if r == nil {
		return nil
	}
	a := &r.Auth
	items := []pickItem{
		{"inherit", "use the folder's or the collection's auth"}, {"none", "send no auth"},
		{"basic", "username and password"}, {"bearer", "token"}, {"apikey", "key and value in a header or query"},
		{"digest", "HTTP Digest"}, {"ntlm", "NTLM domain authentication"}, {"oauth1", "OAuth 1 request signing"}, {"oauth2", "OAuth 2 tokens and browser login"},
		{"awsv4", "AWS Signature Version 4"}, {"wsse", "WSSE UsernameToken"}, {"akamai-edgegrid", "Akamai EdgeGrid request signing"},
	}
	return m.choose("Auth mode", items, slices.Index(authModes, a.Mode), func(i int) tea.Cmd {
		if authModes[i] != a.Mode {
			*a = collection.Auth{Mode: authModes[i], Username: a.Username, Password: a.Password, Token: a.Token, Key: a.Key, Value: a.Value, Placement: a.Placement}
			a.Options = map[string]string{}
			for key, value := range authDefaults(a.Mode) {
				a.Options[key] = value
			}
			if _, ok := a.Options["username"]; ok {
				a.Options["username"] = a.Username
			}
			if _, ok := a.Options["password"]; ok {
				a.Options["password"] = a.Password
			}
			m.changed(r)
		}
		m.focus = focusRequest
		m.req.switchTab(tabAuth)
		return nil
	})
}

func authTab(m *Model, r *collection.Request) tabSpec {
	a := &r.Auth
	onChange := func() { m.changed(r) }
	spec := tabSpec{edit: m.pickAuthMode, editWhat: "auth mode"}
	spec.rows = append(spec.rows, row{line: stMuted.Render("mode ") + stBold.Render(a.Mode) + stMuted.Render(" ▾"), click: m.pickAuthMode})
	authField := func(label string, v *string, secret bool) row {
		shown := markVars(*v)
		if secret && !m.reveal && !strings.Contains(*v, "{{") && *v != "" {
			shown = "••••••"
		}
		return row{line: fmt.Sprintf("%-10s %s", label, shown), sel: true, what: label, edit: func() tea.Cmd {
			f := textField(label, *v)
			if secret {
				f = secretField(label, *v, !m.reveal)
			}
			return m.openForm("Auth "+label, "", []*field{f}, 0, func(vals []string) tea.Cmd {
				*v = vals[0]
				onChange()
				return nil
			})
		}}
	}
	switch a.Mode {
	case "inherit":
		eff, src := runner.EffectiveAuth(r)
		spec.rows = append(spec.rows, text(stMuted.Render("uses ")+stBold.Render(eff.Mode)+stMuted.Render(" from "+src)))
		scope := m.s.Scope(r)
		show := func(label, v string, secret bool) {
			resolved := scope.Interpolate(v)
			if secret && !m.reveal {
				resolved = strings.Repeat("•", min(len(resolved), 8))
			}
			spec.rows = append(spec.rows, text(fmt.Sprintf("  %-10s %s %s %s", label, markVars(v), stMuted.Render("→"), resolved)))
		}
		switch eff.Mode {
		case "basic":
			show("username", eff.Username, false)
			show("password", eff.Password, true)
		case "bearer":
			show("token", eff.Token, true)
		case "apikey":
			show(eff.Key, eff.Value, true)
		}
	case "none":
		spec.rows = append(spec.rows, text(stMuted.Render("No auth.")))
	case "basic":
		spec.rows = append(spec.rows, authField("username", &a.Username, false), authField("password", &a.Password, true))
	case "bearer":
		spec.rows = append(spec.rows, authField("token", &a.Token, true))
	case "apikey":
		place := row{line: fmt.Sprintf("%-10s %s", "add to", cmp.Or(a.Placement, "header")), sel: true, what: "placement", toggle: func() {
			a.Placement = map[string]string{"query": "header"}[a.Placement]
			if a.Placement == "" {
				a.Placement = "query"
			}
			onChange()
		}}
		spec.rows = append(spec.rows, authField("key", &a.Key, false), authField("value", &a.Value, true), place)
	default:
		if !a.Supported() {
			spec.rows = append(spec.rows, text(stWarn.Render("This auth mode is not supported; the request will not be sent.")))
			break
		}
		if a.Options == nil {
			a.Options = map[string]string{}
		}
		keys := make([]string, 0, len(a.Options))
		for key := range a.Options {
			keys = append(keys, key)
		}
		for key := range authDefaults(a.Mode) {
			if !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			shown := a.Options[key]
			secret := runner.LooksSecret(key)
			if secret && !m.reveal && !strings.Contains(shown, "{{") && shown != "" {
				shown = "••••••"
			}
			spec.rows = append(spec.rows, row{line: key + "  " + markVars(shown), sel: true, what: key, edit: func() tea.Cmd {
				value := a.Options[key]
				f := textField(key, value)
				if secret {
					f = secretField(key, value, !m.reveal)
				}
				if value == "true" || value == "false" {
					f = choiceField(key, []string{"true", "false"}, value)
				}
				return m.openForm("Auth "+key, "", []*field{f}, 0, func(values []string) tea.Cmd { a.Options[key] = values[0]; onChange(); return nil })
			}})
		}
	}
	return spec
}

func authDefaults(mode string) map[string]string {
	options := map[string]string{}
	keys := map[string][]string{
		"digest": {"username", "password"}, "ntlm": {"username", "password", "domain"}, "wsse": {"username", "password"},
		"oauth1":          {"consumerKey", "consumerSecret", "accessToken", "accessTokenSecret", "signatureMethod", "privateKey", "privateKeyType", "placement"},
		"oauth2":          {"flow", "accessTokenUrl", "authorizationUrl", "callbackUrl", "credentials.clientId", "credentials.clientSecret", "scope"},
		"awsv4":           {"accessKeyId", "secretAccessKey", "sessionToken", "region", "service", "profileName"},
		"akamai-edgegrid": {"clientToken", "clientSecret", "accessToken", "headersToSign"},
	}
	for _, key := range keys[mode] {
		options[key] = ""
	}
	if mode == "oauth1" {
		options["signatureMethod"] = "HMAC-SHA1"
		options["placement"] = "header"
	}
	if mode == "oauth2" {
		options["flow"] = "client_credentials"
	}
	return options
}
