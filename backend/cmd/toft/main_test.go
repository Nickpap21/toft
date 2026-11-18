package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A deploy by project/environment/name, followed to its end; a refused
// deploy prints the problem's detail and code and exits 1.
func TestDeployWaitAndProblem(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer paas_k" || r.Header.Get("X-Toft-Org") != "org-1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"storefront"}]}`))
		case "GET /api/v1/workspace/services":
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"api","project_id":"p1","environment_name":"production"},{"id":"s2","name":"api","project_id":"p1","environment_name":"staging"}]}`))
		case "POST /api/v1/services/s2/deployments":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["image_tag"] != "1.2.3" {
				t.Errorf("image_tag = %v", in["image_tag"])
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":"d1","service_id":"s2","status":"pending"}}`))
		case "GET /api/v1/services/s2/deployments/d1/logs/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: log\ndata: {\"message\":\"pulling 1.2.3\"}\n\nevent: done\ndata: {\"status\":\"running\"}\n\n"))
		case "POST /api/v1/services/s1/deployments":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"the saved configuration breaks the deploy policy","detail":"the saved configuration breaks the deploy policy","code":"deploy_policy_violation","status":422,"data":{"code":"deploy_policy_violation","violations":[{"rule":"docker_socket","message":"mounts the Docker socket","field":"volumes","service":""}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := map[string]string{"TOFT_URL": srv.URL, "TOFT_API_KEY": "paas_k", "TOFT_ORG": "org-1"}
	getenv := func(k string) string { return env[k] }

	var out, errOut bytes.Buffer
	code := run([]string{"deploy", "storefront/staging/api", "--image", "1.2.3", "--wait"}, getenv, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"deployment d1 started (pending)", "pulling 1.2.3", "deployment d1: running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	errOut.Reset()
	code = run([]string{"deploy", "storefront/production/api"}, getenv, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "deploy_policy_violation") || !strings.Contains(errOut.String(), "docker_socket: mounts the Docker socket") {
		t.Errorf("stderr = %s", errOut.String())
	}

	if code := run([]string{"deploy"}, getenv, &out, &errOut); code != 2 {
		t.Errorf("usage exit = %d", code)
	}
	if code := run([]string{"deploy", "x"}, func(string) string { return "" }, &out, &errOut); code != 2 {
		t.Errorf("missing config exit = %d", code)
	}
}

// --ref sends what the ref is, a branch unless --ref-type says tag: the
// server refuses a git_ref without it.
func TestDeployRefSendsItsType(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"storefront"}]}`))
		case "GET /api/v1/workspace/services":
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"api","project_id":"p1","environment_name":"production"}]}`))
		case "POST /api/v1/services/s1/deployments":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			bodies = append(bodies, in)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"id":"d1","service_id":"s1","status":"pending"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	env := map[string]string{"TOFT_URL": srv.URL, "TOFT_API_KEY": "paas_k", "TOFT_ORG": "org-1"}
	getenv := func(k string) string { return env[k] }

	var out, errOut bytes.Buffer
	for _, args := range [][]string{{"--ref", "feature/login"}, {"--ref", "v1.2.0", "--ref-type", "tag"}} {
		if code := run(append([]string{"deploy", "storefront/production/api"}, args...), getenv, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
	}
	want := []map[string]any{
		{"git_ref": "feature/login", "git_ref_type": "branch", "server_id": nil},
		{"git_ref": "v1.2.0", "git_ref_type": "tag", "server_id": nil},
	}
	if got, _ := json.Marshal(bodies); string(got) != mustJSON(t, want) {
		t.Errorf("bodies = %s, want %s", got, mustJSON(t, want))
	}
	for _, args := range [][]string{{"--ref", "v1", "--ref-type", "commit"}, {"--ref-type", "tag"}, {"--image", "1.2.3", "--ref-type", "tag"}} {
		if code := run(append([]string{"deploy", "storefront/production/api"}, args...), getenv, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d, want the usage error", args, code)
		}
	}
	if len(bodies) != 2 {
		t.Errorf("a refused flag sent a deploy: %d bodies", len(bodies))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// toft logs with a key that lacks the logs scope says which scope it needs,
// not just that the key is limited, whether the name lookup or the stream
// refuses it (issue #11); another 403 passes through as it is.
func TestLogsNamesTheMissingScope(t *testing.T) {
	refuse := func(w http.ResponseWriter, code string) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"refused","status":403,"data":{"code":"` + code + `"}}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch key := r.Header.Get("Authorization"); {
		case key == "Bearer paas_vars" && r.URL.Path == "/api/v1/projects":
			refuse(w, "insufficient_scope")
			return
		case key == "Bearer paas_other" && r.URL.Path == "/api/v1/projects":
			refuse(w, "forbidden")
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects":
			_, _ = w.Write([]byte(`{"data":[{"id":"p1","name":"storefront"}]}`))
		case "GET /api/v1/workspace/services":
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","name":"api","project_id":"p1","environment_name":"production"}]}`))
		case "GET /api/v1/services/s1/logs/live":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"this API key is limited to its scopes in one project","status":403,"data":{"code":"insufficient_scope"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for key, hint := range map[string]bool{"paas_k": true, "paas_vars": true, "paas_other": false} {
		env := map[string]string{"TOFT_URL": srv.URL, "TOFT_API_KEY": key, "TOFT_ORG": "org-1"}
		var out, errOut bytes.Buffer
		if code := run([]string{"logs", "storefront/production/api"}, func(k string) string { return env[k] }, &out, &errOut); code != 1 {
			t.Fatalf("%s: exit %d, want 1: %s", key, code, errOut.String())
		}
		if got := strings.Contains(errOut.String(), `"logs" scope (Read live logs)`); got != hint {
			t.Errorf("%s: hint %v, want %v; stderr = %s", key, got, hint, errOut.String())
		}
	}
}
