package codehost

// Azure DevOps' REST API as tm asks it (docs/SPEC.md §7.5): GETs through
// az rest with a Microsoft Entra token of az login, the way Microsoft
// documents ad hoc REST calls (learn.microsoft.com/azure/devops/cli/
// entra-tokens), else with the PAT of AZURE_DEVOPS_EXT_PAT. Not through
// the azure-devops extension: its SDK sends X-VSS-ForceMsaPassThrough,
// which signs a work login whose email is also a personal Microsoft
// account in as that account, one the organization doesn't have
// (TF400813). The token is the same as the extension's, so Conditional
// Access, which Entra applies when az login gets it, holds alike.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// azResource is Azure DevOps' application id in Microsoft Entra ID, the
// resource az rest asks az login for a token to.
const azResource = "499b84ac-1321-427f-aa17-267ca6975798"

// patEnv names the PAT the azure-devops extension reads too.
const patEnv = "AZURE_DEVOPS_EXT_PAT"

// azSubs is, per organization URL, the az subscription whose tenant is
// the organization's, when az login's default is another tenant's: what
// az rest gets its token for (--subscription).
var azSubs sync.Map

// get answers the JSON of the GET of u, a REST URL under the Target's
// organization: through az rest, retried once with the subscription of
// the organization's tenant when az's default tenant is refused, else
// with the PAT when az can't sign in or is refused and one is set.
func (a Azure) get(dir, u string) ([]byte, error) {
	out, err := a.rest(dir, u)
	if err == nil || !authFailure(err) {
		return out, err
	}
	if _, cached := azSubs.Load(a.Target.OrgURL); !cached && isRefused(err) {
		if sub, _, _ := a.tenantAccount(dir); sub != "" {
			azSubs.Store(a.Target.OrgURL, sub)
			if out, err2 := a.rest(dir, u); err2 == nil || !authFailure(err2) {
				return out, err2
			}
		}
	}
	if pat := a.getenv(patEnv); pat != "" {
		return a.patGet(u, pat)
	}
	return nil, err
}

// rest runs az rest's GET of u with JSON asked for.
func (a Azure) rest(dir, u string, headers ...string) ([]byte, error) {
	args := []string{"rest", "--method", "get", "--resource", azResource, "--url", u, "--headers", "Accept=application/json"}
	args = append(args, headers...)
	if sub, ok := azSubs.Load(a.Target.OrgURL); ok {
		args = append(args, "--subscription", sub.(string))
	}
	return a.az(dir, args...)
}

func (a Azure) getenv(k string) string {
	if a.Getenv != nil {
		return a.Getenv(k)
	}
	return os.Getenv(k)
}

// authFailure is an az failure a PAT or another tenant may get past: az
// missing or not logged in, or Azure DevOps refusing its user.
func authFailure(err error) bool {
	var ce *CLIError
	return errors.As(err, &ce) && ce.auth
}

// isRefused is Azure DevOps refusing az's token (401/403, TF400813), not
// az lacking one.
func isRefused(err error) bool {
	var ce *CLIError
	return errors.As(err, &ce) && ce.auth && ce.Problem != "az is not logged in" && ce.Problem != "az is not installed"
}

var guidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// orgTenant is the Microsoft Entra tenant of the organization at
// orgURL, as its anonymous answer names it (X-VSS-ResourceTenant), ""
// when it can't tell or the organization has none. A variable for tests.
var orgTenant = func(orgURL string) string {
	if !azOrgURLRE.MatchString(orgURL) {
		return ""
	}
	c := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(orgURL + "/_apis/connectionData")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	t := resp.Header.Get("X-VSS-ResourceTenant")
	if !guidRE.MatchString(t) || t == "00000000-0000-0000-0000-000000000000" {
		return ""
	}
	return strings.ToLower(t)
}

// tenantAccount is the organization's tenant ("" when it can't tell),
// whether az login has an account in it, and, when az's default
// subscription is another tenant's, the id of one of az's subscriptions
// in it, for az rest's --subscription.
func (a Azure) tenantAccount(dir string) (sub, tenant string, has bool) {
	if tenant = orgTenant(a.Target.OrgURL); tenant == "" {
		return "", "", false
	}
	out, err := a.az(dir, "account", "list", "--query", "[].{id:id,tenantId:tenantId,isDefault:isDefault}")
	if err != nil {
		return "", tenant, false
	}
	var subs []struct {
		ID        string `json:"id"`
		TenantID  string `json:"tenantId"`
		IsDefault bool   `json:"isDefault"`
	}
	json.Unmarshal(out, &subs)
	for _, s := range subs {
		if s.IsDefault && strings.EqualFold(s.TenantID, tenant) {
			return "", tenant, true
		}
	}
	for _, s := range subs {
		if strings.EqualFold(s.TenantID, tenant) && guidRE.MatchString(s.ID) {
			return s.ID, tenant, true
		}
	}
	return "", tenant, false
}

// patGet is a's PATGet, else an HTTPS GET of u with the PAT, as Basic
// auth with an empty user, never on a command line. Its errors are
// CLIErrors named as az's are.
func (a Azure) patGet(u, pat string) ([]byte, error) {
	if a.PATGet != nil {
		return a.PATGet(u, pat)
	}
	return PATGet(u, pat)
}

// PATGet asks u with pat (patGet).
func PATGet(u, pat string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, azError(err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+pat)))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")
	c := http.Client{Timeout: azTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return nil, azError(fmt.Errorf("GET %s with %s: %w", u, patEnv, err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, azError(fmt.Errorf("GET %s with %s: %w", u, patEnv, err))
	}
	if resp.StatusCode == http.StatusOK {
		return body, nil
	}
	var msg struct {
		Message string `json:"message"`
	}
	json.Unmarshal(body, &msg)
	err = fmt.Errorf("GET %s with %s: HTTP %d %s", u, patEnv, resp.StatusCode, firstLineOf(msg.Message))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusFound, http.StatusNonAuthoritativeInfo:
		return nil, &CLIError{CLI: "az", Err: err, Problem: "Azure DevOps refused " + patEnv + " (401/403)",
			Advice: "the user checks that " + patEnv + " hasn't expired and has the Code and Build read scopes (tm doctor)"}
	}
	return nil, azError(err)
}

func firstLineOf(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
